#!/usr/bin/env python3
"""NTM discovery agent (Python).

Four-phase discovery of the configured ranges, storing each phase on the
platform as it runs. Phase 1 (ping) is a native ICMP echo sweep in Python; the
port scan (phases 3-4) is a pure-Python TCP connect scan that also grabs the
service banner / HTTP Server header (the "platform" published on each port) and
infers the host OS from the ping TTL refined by those banners. No nmap.

    1. ping     ICMP echo to every address of the ranges; live hosts stored at once
    2. hostname reverse DNS (PTR) of the live hosts
    3. ports    TCP connect scan + banner/header grab of the live hosts
    4. detect   OS guess (ping TTL + banner hints) of the hosts with open ports

Config comes from environment variables (set at install time), matching the
previous agent: NTM_PLATFORM_URL, NTM_INSTALL_TOKEN, NTM_RANGES, NTM_AGENT_NAME,
NTM_STATE_PATH, NTM_TLS_INSECURE, NTM_SCAN_INTERVAL, NTM_POLL_INTERVAL,
NTM_PING_RATE, NTM_PING_TIMEOUT, NTM_DNS_RESOLVER, plus NTM_PORT_WORKERS and
NTM_CONNECT_TIMEOUT for the Python port scan.
"""
import ipaddress
import json
import os
import select
import socket
import ssl
import struct
import sys
import threading
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor

# Top TCP ports the port scanner checks on each live host, with a friendly
# service name used when no banner identifies the product.
TOP_PORTS = {
    21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "domain", 80: "http",
    110: "pop3", 111: "rpcbind", 135: "msrpc", 139: "netbios-ssn", 143: "imap",
    389: "ldap", 443: "https", 445: "microsoft-ds", 465: "smtps", 587: "submission",
    631: "ipp", 636: "ldaps", 993: "imaps", 995: "pop3s", 1433: "ms-sql-s",
    1521: "oracle", 2049: "nfs", 2375: "docker", 3000: "http-alt", 3306: "mysql",
    3389: "ms-wbt-server", 5000: "http-alt", 5432: "postgresql", 5900: "vnc",
    5985: "wsman", 6379: "redis", 8000: "http-alt", 8080: "http-proxy",
    8081: "http-alt", 8443: "https-alt", 9000: "http-alt", 9100: "jetdirect",
    9200: "elasticsearch", 27017: "mongodb",
}
HTTP_PORTS = {80, 3000, 5000, 8000, 8080, 8081, 9000, 9200, 5985}  # 5985 = WinRM (HTTP)
HTTPS_PORTS = {443, 8443, 465, 993, 995, 636}
# Ports that strongly indicate a Windows host (MS-RPC, RDP, WinRM, SMB combo).
WINDOWS_PORTS = {135, 3389, 5985, 5986}


def env(key, default=""):
    v = os.environ.get(key, "")
    return v if v != "" else default


def build_excludes(cfg):
    """Networks to skip: NTM_EXCLUDE plus the agent's own host and the platform
    (NTM infrastructure must not show up as discovered assets)."""
    nets = []
    for c in cfg.exclude:
        try:
            nets.append(ipaddress.ip_network(c, strict=False))
        except ValueError:
            log("ignoring invalid NTM_EXCLUDE entry: %s" % c)
    for ip in (local_ip(), platform_ip(cfg)):
        if ip:
            try:
                nets.append(ipaddress.ip_network(ip + "/32"))
            except ValueError:
                pass
    return nets


def is_excluded(ip, nets):
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return False
    return any(addr in n for n in nets)


def platform_ip(cfg):
    """Resolve the platform host (from NTM_PLATFORM_URL) to an IP, so the agent
    never inventories the platform it reports to."""
    try:
        host = cfg.platform_url.split("://", 1)[-1].split("/", 1)[0].split(":", 1)[0]
        return socket.gethostbyname(host)
    except (OSError, IndexError):
        return ""


def local_ip():
    """Best-effort primary IPv4 of this host (the interface used for outbound
    traffic). Reported to the platform so the UI can suggest a scan range."""
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("10.255.255.255", 1))
        return s.getsockname()[0]
    except OSError:
        return ""
    finally:
        s.close()


class Config:
    def __init__(self):
        self.platform_url = env("NTM_PLATFORM_URL", "https://localhost:8443").rstrip("/")
        self.install_token = env("NTM_INSTALL_TOKEN", "")
        self.agent_name = env("NTM_AGENT_NAME", socket.gethostname() or "ntm-agent")
        self.ranges = [r.strip() for r in env("NTM_RANGES", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16").split(",") if r.strip()]
        self.state_path = env("NTM_STATE_PATH", "/var/lib/ntm/agent-state.json")
        self.insecure = env("NTM_TLS_INSECURE", "true") == "true"
        self.interval = parse_duration(env("NTM_SCAN_INTERVAL", "30m"), 1800)
        self.poll = parse_duration(env("NTM_POLL_INTERVAL", "5s"), 5)
        self.ping_rate = max(1, int(env("NTM_PING_RATE", "1000")))
        self.ping_timeout = parse_duration(env("NTM_PING_TIMEOUT", "2s"), 2)
        self.resolver = env("NTM_DNS_RESOLVER", "")
        self.port_workers = max(1, int(env("NTM_PORT_WORKERS", "200")))
        self.connect_timeout = float(env("NTM_CONNECT_TIMEOUT", "1.0"))
        # Networks/IPs to never inventory (e.g. the Docker infra network). The
        # agent's own host and the platform are always excluded on top of this.
        self.exclude = [r.strip() for r in env("NTM_EXCLUDE", "").split(",") if r.strip()]


def parse_duration(s, default):
    """Parse a Go-style duration (e.g. 30m, 5s, 2s) into seconds."""
    s = s.strip()
    try:
        if s.endswith("ms"):
            return float(s[:-2]) / 1000
        if s.endswith("m"):
            return float(s[:-1]) * 60
        if s.endswith("h"):
            return float(s[:-1]) * 3600
        if s.endswith("s"):
            return float(s[:-1])
        return float(s)
    except ValueError:
        return default


def log(msg):
    print("[ntm-agent] " + msg, flush=True)


# --- Platform HTTP client -------------------------------------------------

class Platform:
    def __init__(self, cfg):
        self.cfg = cfg
        self.token = None
        if cfg.insecure:
            self.ctx = ssl.create_default_context()
            self.ctx.check_hostname = False
            self.ctx.verify_mode = ssl.CERT_NONE
        else:
            self.ctx = ssl.create_default_context()

    def _do(self, method, path, body=None, auth=True):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.cfg.platform_url + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if auth and self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        with urllib.request.urlopen(req, context=self.ctx, timeout=60) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})

    def enroll(self):
        """Load a saved token or enroll with the install token."""
        st = read_state(self.cfg.state_path)
        if st and st.get("agent_token"):
            self.token = st["agent_token"]
            return st["agent_id"]
        status, out = self._do("POST", "/api/v1/enroll", {
            "install_token": self.cfg.install_token,
            "name": self.cfg.agent_name,
            "ranges": self.cfg.ranges,
            "ip": local_ip(),
        }, auth=False)
        self.token = out["agent_token"]
        write_state(self.cfg.state_path, out)
        return out["agent_id"]

    def status(self, state, phase="", scan_range=""):
        body = {"state": state, "phase": phase, "ip": local_ip()}
        if scan_range:
            body["range"] = scan_range
        try:
            self._do("POST", "/api/v1/agent/status", body)
        except Exception:
            pass  # best-effort; the phase uploads also move the UI

    def upload_phase(self, phase, hosts, dns=None):
        try:
            self._do("POST", "/api/v1/scan-phase", {"phase": phase, "hosts": hosts, "dns": dns or []})
        except Exception as e:
            log("phase %s: upload failed: %s" % (phase, e))

    def commands(self):
        try:
            _, out = self._do("GET", "/api/v1/agent/commands")
            return bool(out.get("scan_now")), bool(out.get("stop_now")), out.get("scan_range", "")
        except Exception:
            return False, False, ""


def read_state(path):
    try:
        with open(path) as f:
            return json.load(f)
    except Exception:
        return None


def write_state(path, st):
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            json.dump(st, f)
        os.chmod(path, 0o600)
    except Exception as e:
        log("could not persist state: %s" % e)


# --- Phase 1: native ICMP echo ping sweep ---------------------------------

def icmp_checksum(b):
    if len(b) % 2:
        b += b"\x00"
    s = sum(struct.unpack("!%dH" % (len(b) // 2), b))
    s = (s >> 16) + (s & 0xffff)
    s += s >> 16
    return (~s) & 0xffff


def echo_request(ident, seq):
    hdr = struct.pack("!BBHHH", 8, 0, 0, ident, seq)
    payload = b"ntm-ping"
    return struct.pack("!BBHHH", 8, 0, icmp_checksum(hdr + payload), ident, seq) + payload


def ping_sweep(cfg, ranges, stop, on_batch, exclude_nets=None):
    exclude_nets = exclude_nets or []
    """Send one ICMP echo to every host address of the ranges, streaming the
    hosts that reply (with their TTL, used later for OS detection) to on_batch.
    Returns {ip: ttl}. Requires a raw socket (root / CAP_NET_RAW)."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_ICMP)
    sock.setblocking(False)
    ident = os.getpid() & 0xffff
    ttl_by_ip = {}
    lock = threading.Lock()
    pending = []

    def flush():
        with lock:
            batch, pending[:] = list(pending), []
        if batch:
            on_batch(batch)

    done = threading.Event()

    def receiver():
        last_flush = time.time()
        while not done.is_set():
            r, _, _ = select.select([sock], [], [], 0.5)
            if r:
                try:
                    pkt, src = sock.recvfrom(2048)
                except OSError:
                    continue
                ip = src[0]
                ihl = (pkt[0] & 0x0f) * 4
                if len(pkt) < ihl + 8 or pkt[ihl] != 0:  # 0 = echo reply
                    continue
                if struct.unpack("!H", pkt[ihl + 4:ihl + 6])[0] != ident:
                    continue
                if ip in ttl_by_ip or is_excluded(ip, exclude_nets):
                    continue
                ttl = pkt[8]
                ttl_by_ip[ip] = ttl
                with lock:
                    pending.append({"ip": ip, "status": "up"})
            if time.time() - last_flush >= 2:
                flush()
                last_flush = time.time()
        flush()

    rx = threading.Thread(target=receiver, daemon=True)
    rx.start()

    slot = 0.01
    per_slot = max(1, int(cfg.ping_rate * slot))
    seq = 0
    sent_in_slot = 0
    next_slot = time.time() + slot
    for cidr in ranges:
        net = ipaddress.ip_network(cidr, strict=False)
        for host in net.hosts():
            if stop.is_set():
                break
            seq = (seq + 1) & 0xffff
            try:
                sock.sendto(echo_request(ident, seq), (str(host), 0))
            except OSError:
                pass
            sent_in_slot += 1
            if sent_in_slot >= per_slot:
                delay = next_slot - time.time()
                if delay > 0:
                    time.sleep(delay)
                sent_in_slot = 0
                next_slot += slot
        if stop.is_set():
            break

    if not stop.is_set():
        time.sleep(cfg.ping_timeout)
    done.set()
    rx.join(timeout=3)
    sock.close()
    return ttl_by_ip


# --- Phase 3/4: Python TCP connect scan with banner + OS detection --------

def grab_banner(ip, port, timeout):
    """Open a TCP connection and return (open, banner_text). For HTTP ports the
    banner is the Server response header; otherwise it is the service's own
    greeting. Returns (False, '') when the port is closed/filtered."""
    try:
        with socket.create_connection((ip, port), timeout=timeout) as s:
            s.settimeout(timeout)
            if port in HTTPS_PORTS:
                ctx = ssl.create_default_context()
                ctx.check_hostname = False
                ctx.verify_mode = ssl.CERT_NONE
                try:
                    s = ctx.wrap_socket(s, server_hostname=ip)
                except ssl.SSLError:
                    return True, ""  # TLS port but handshake refused the probe
            if port in HTTP_PORTS or port in HTTPS_PORTS:
                try:
                    s.sendall(b"HEAD / HTTP/1.0\r\nHost: %s\r\n\r\n" % ip.encode())
                    data = s.recv(2048).decode("latin-1", "replace")
                except OSError:
                    return True, ""
                return True, http_server(data)
            try:
                data = s.recv(256).decode("latin-1", "replace")
            except OSError:
                data = ""
            return True, data.strip()
    except (socket.timeout, OSError):
        return False, ""


def http_server(response):
    for line in response.split("\r\n"):
        if line.lower().startswith("server:"):
            return "HTTP " + line.split(":", 1)[1].strip()
    first = response.split("\r\n", 1)[0]
    return first if first.startswith("HTTP") else ""


def parse_product(service, banner):
    """Split a banner into (product, version) for display in the CMDB."""
    if not banner:
        return "", ""
    b = banner.strip()
    if b.lower().startswith("ssh-"):
        parts = b.split("-", 2)
        sw = parts[2] if len(parts) > 2 else b
        name = sw.split()[0] if sw else sw
        if "_" in name:
            p, v = name.split("_", 1)
            return p, v
        return name, ""
    if b.startswith("HTTP "):
        srv = b[5:]
        if "/" in srv:
            p, v = srv.split("/", 1)
            return p, v.split()[0]
        return srv, ""
    return b[:80], ""


def os_from_ttl(ttl):
    if ttl is None:
        return ""
    if ttl <= 64:
        return "Linux/Unix"
    if ttl <= 128:
        return "Windows"
    return "Network device"


def guess_os(ttl, open_ports, banners):
    """Infer the host OS from the open-port signature, the service banners, and
    the ping TTL, in that order of confidence. Port signatures matter because
    the strongest Windows tell (135/3389/445/WinRM) emits no banner, and because
    TTL is unreliable behind NAT (e.g. Docker Desktop flattens it to 64)."""
    blob = " ".join(banners).lower()
    # Banner hints — most specific.
    if "microsoft" in blob or "windows" in blob or "iis" in blob:
        return "Windows"
    if "ubuntu" in blob:
        return "Linux (Ubuntu)"
    if "debian" in blob:
        return "Linux (Debian)"
    if "centos" in blob or "red hat" in blob or "rhel" in blob:
        return "Linux (RHEL)"
    if "raspbian" in blob:
        return "Linux (Raspbian)"
    # Port signature — Windows services that carry no banner.
    pset = set(open_ports)
    if pset & WINDOWS_PORTS or (445 in pset and 139 in pset):
        return "Windows"
    # Generic Unix service banners.
    if any(k in blob for k in ("openssh", "nginx", "apache", "linux")):
        return "Linux/Unix"
    # TTL fallback (least reliable).
    return os_from_ttl(ttl)


def scan_host_ports(ip, ttl, cfg, stop):
    """Connect-scan TOP_PORTS on one host, grabbing a banner per open port, and
    return (ports, os_guess). os_guess combines the ping TTL with banner hints."""
    ports = []
    banners = []
    for port, name in sorted(TOP_PORTS.items()):
        if stop.is_set():
            break
        is_open, banner = grab_banner(ip, port, cfg.connect_timeout)
        if not is_open:
            continue
        product, version = parse_product(name, banner)
        service = name
        if port in HTTP_PORTS:
            service = "http"
        elif port in HTTPS_PORTS:
            service = "https"
        ports.append({
            "port": port, "protocol": "tcp", "state": "open",
            "service": service, "product": product, "version": version,
        })
        if banner:
            banners.append(banner)
    os_guess = guess_os(ttl, [p["port"] for p in ports], banners)
    return ports, os_guess


# --- Scan orchestration ---------------------------------------------------

PHASE_BATCH = 64


def run_scan(cfg, plat, stop, ranges=None):
    ranges = ranges or cfg.ranges
    start = time.time()
    plat.status("running", "ping", ",".join(ranges))

    # Phase 1 — native ICMP ping. nmap is never run here or during the sweep.
    exclude_nets = build_excludes(cfg)
    if exclude_nets:
        log("excluding from inventory: %s" % ", ".join(str(n) for n in exclude_nets))
    log("phase 1/4 ping: ICMP echo to %s at %d/s" % (" ".join(ranges), cfg.ping_rate))
    live = []

    def on_ping(batch):
        plat.upload_phase("ping", batch)
        live.extend(h["ip"] for h in batch)
        log("phase 1/4 ping: %d live host(s) stored so far" % len(live))

    ttl_by_ip = ping_sweep(cfg, ranges, stop, on_ping, exclude_nets)
    if stop.is_set():
        log("scan stopped during ping — stored hosts are kept")
        plat.status("idle")
        return
    log("phase 1/4 ping done: %d live host(s) (%ds)" % (len(live), time.time() - start))

    # Phase 2 — hostname (reverse DNS).
    plat.status("running", "hostname")
    named = 0
    for batch in chunks(live, PHASE_BATCH):
        hosts, dns = resolve_hostnames(batch)
        if hosts or dns:
            plat.upload_phase("hostname", hosts, dns)
        named += len(hosts)
        if stop.is_set():
            break
    if stop.is_set():
        plat.status("idle")
        return
    log("phase 2/4 hostname done: %d of %d named" % (named, len(live)))

    # Phase 3 — Python TCP port scan with banner/header grab.
    plat.status("running", "ports")
    log("phase 3/4 ports: Python connect scan of %d port(s)/host, %d workers" % (len(TOP_PORTS), cfg.port_workers))
    results = {}  # ip -> (ports, os_guess)
    with_ports = 0
    with ThreadPoolExecutor(max_workers=cfg.port_workers) as pool:
        for batch in chunks(live, max(cfg.port_workers, PHASE_BATCH)):
            if stop.is_set():
                break
            futures = {ip: pool.submit(scan_host_ports, ip, ttl_by_ip.get(ip), cfg, stop) for ip in batch}
            host_ports = []
            for ip, fut in futures.items():
                ports, os_guess = fut.result()
                results[ip] = (ports, os_guess)
                if ports:
                    host_ports.append({"ip": ip, "status": "up", "ports": ports})
            if host_ports:
                with_ports += len(host_ports)
                plat.upload_phase("ports", host_ports)
    if stop.is_set():
        plat.status("idle")
        return
    log("phase 3/4 ports done: %d host(s) with open ports" % with_ports)

    # Phase 4 — OS + published platform of the hosts that have open ports.
    plat.status("running", "detect")
    detected = 0
    detect_hosts = []
    for ip, (ports, os_guess) in results.items():
        if not ports:
            continue
        detect_hosts.append({"ip": ip, "status": "up", "os_guess": os_guess, "ports": ports})
    for batch in chunks(detect_hosts, PHASE_BATCH):
        if stop.is_set():
            break
        plat.upload_phase("detect", batch)
        detected += len(batch)
    if stop.is_set():
        plat.status("idle")
        return
    log("phase 4/4 detect done: %d host(s) fingerprinted" % detected)

    plat.status("idle")
    log("scan complete in %ds: %d live, %d named, %d with open ports" % (time.time() - start, len(live), named, with_ports))


def resolve_hostnames(ips):
    hosts, dns, seen = [], [], set()
    for ip in ips:
        try:
            name, aliases, addrs = socket.gethostbyaddr(ip)
        except (socket.herror, socket.gaierror, OSError):
            continue
        hosts.append({"ip": ip, "hostname": name})
        for key in [("PTR", name, ip)] + [("A", name, a) for a in addrs]:
            if key not in seen:
                seen.add(key)
                dns.append({"host_ip": ip, "name": key[1], "type": key[0], "value": key[2]})
    return hosts, dns


def chunks(items, n):
    for i in range(0, len(items), n):
        yield items[i:i + n]


# --- Main loop ------------------------------------------------------------

def main():
    cfg = Config()
    plat = Platform(cfg)

    while True:
        try:
            agent_id = plat.enroll()
            break
        except Exception as e:
            log("enrollment failed: %s (retrying in 5s)" % e)
            time.sleep(5)
    log("enrolled as agent #%s, scanning ranges: %s" % (agent_id, " ".join(cfg.ranges)))

    state = {"running": False, "stop": threading.Event()}

    def launch(trigger, ranges=None):
        if state["running"]:
            return
        state["running"] = True
        state["stop"] = threading.Event()
        log("starting scan (%s) on %s" % (trigger, " ".join(ranges or cfg.ranges)))

        def worker():
            try:
                run_scan(cfg, plat, state["stop"], ranges)
            except Exception as e:
                log("scan error: %s" % e)
                plat.status("idle")
            finally:
                state["running"] = False
        threading.Thread(target=worker, daemon=True).start()

    launch("startup")
    last_full = time.time()
    log("polling for commands every %ss; periodic scan every %ss" % (int(cfg.poll), int(cfg.interval)))

    while True:
        time.sleep(cfg.poll)
        scan_now, stop_now, scan_range = plat.commands()
        if stop_now and state["running"]:
            log("STOP requested — cancelling running scan")
            state["stop"].set()
        if scan_now:
            ranges = [r.strip() for r in scan_range.split(",") if r.strip()] or None
            launch("on-demand", ranges)
            last_full = time.time()
        if time.time() - last_full >= cfg.interval and not state["running"]:
            launch("scheduled")
            last_full = time.time()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)

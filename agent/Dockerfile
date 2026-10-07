# NTM discovery agent (Python). Ping and port scan are implemented in Python
# (stdlib only); no nmap. The ICMP ping sweep needs a raw socket, so the
# container is granted NET_RAW in docker-compose.yml.
FROM python:3.12-alpine
WORKDIR /app
COPY ntm_agent.py /app/ntm_agent.py
RUN mkdir -p /var/lib/ntm
ENTRYPOINT ["python3", "/app/ntm_agent.py"]

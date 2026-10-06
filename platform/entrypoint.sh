#!/bin/sh
set -e

# Generate a self-signed TLS certificate on first boot so the platform serves
# HTTPS out of the box. In production, mount your own cert/key over /certs.
if [ ! -f "$NTM_TLS_CERT" ] || [ ! -f "$NTM_TLS_KEY" ]; then
  echo "[entrypoint] generating self-signed TLS certificate"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "$NTM_TLS_KEY" -out "$NTM_TLS_CERT" -days 825 \
    -subj "/CN=${NTM_TLS_CN:-ntm-platform}" \
    -addext "subjectAltName=DNS:ntm-platform,DNS:localhost,IP:127.0.0.1" 2>/dev/null
fi

exec /app/ntm-platform

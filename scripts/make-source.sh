#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
pkgver=1.0.0
git archive --format=tar --prefix="uniflow-ipp-server-$pkgver/" HEAD |
    gzip -n > "uniflow-ipp-server-$pkgver.tar.gz"
echo "Created uniflow-ipp-server-$pkgver.tar.gz from committed sources."

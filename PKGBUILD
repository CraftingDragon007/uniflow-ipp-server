# Maintainer: uniFLOW IPP Server
pkgname=uniflow-ipp-server
pkgver=1.0.0
pkgrel=1
pkgdesc="IPP server for uniFLOW CUPS integration"
arch=('x86_64')
url=""
license=('MIT')
makedepends=('go>=1.22')
depends=('cups' 'cups-filters' 'glibc')
backup=('etc/uniflow-ipp/config.toml')
install=uniflow-ipp-server.install

source=("$pkgname-$pkgver.tar.gz")
sha256sums=('SKIP')

build() {
    cd "$srcdir/$pkgname-$pkgver"
    go build \
        -trimpath \
        -ldflags "-X main.Version=$pkgver" \
        -o uniflow-ipp-server \
        ./cmd/uniflow-ipp-server
}

check() {
    cd "$srcdir/$pkgname-$pkgver"
    go test ./...
}

package() {
    cd "$srcdir/$pkgname-$pkgver"

    install -Dm755 uniflow-ipp-server "$pkgdir/usr/bin/uniflow-ipp-server"

    install -Dm644 config.toml "$pkgdir/etc/uniflow-ipp/config.toml"
    install -Dm644 README.md "$pkgdir/usr/share/doc/uniflow-ipp-server/README.md"

    install -Dm644 ppd/uniflow-ipp.ppd "$pkgdir/usr/share/uniflow-ipp/ppd/uniflow-ipp.ppd"

    install -Dm755 scripts/install-cups-queue.sh "$pkgdir/usr/share/uniflow-ipp/scripts/install-cups-queue.sh"

    install -Dm644 contrib/systemd/uniflow-ipp.service "$pkgdir/usr/lib/systemd/system/uniflow-ipp.service"

    install -Dm644 contrib/sysusers/uniflow-ipp.conf "$pkgdir/usr/lib/sysusers.d/uniflow-ipp.conf"
    install -Dm644 contrib/tmpfiles/uniflow-ipp.conf "$pkgdir/usr/lib/tmpfiles.d/uniflow-ipp.conf"
}
# Maintainer: uniFLOW IPP Server
pkgname=uniflow-ipp-server
pkgver=1.0.0
pkgrel=1
pkgdesc="IPP server for uniFLOW CUPS integration"
arch=('x86_64')
url="https://github.com/uniflow-ipp-server"
license=('MIT')
makedepends=('go>=1.22')
depends=('cups' 'glibc')
backup=('etc/uniflow-ipp/config.toml')
install=uniflow-ipp-server.install

source=(
    "uniflow-ipp-server-$pkgver.tar.gz::git+https://github.com/uniflow-ipp-server.git#tag=v$pkgver"
)
sha256sums=('SKIP')

build() {
    cd "$srcdir/uniflow-ipp-server-$pkgver"
    go build \
        -ldflags "-X main.Version=$pkgver" \
        -o uniflow-ipp-server \
        ./cmd/uniflow-ipp-server
}

package() {
    cd "$srcdir/uniflow-ipp-server-$pkgver"

    install -Dm755 uniflow-ipp-server "$pkgdir/usr/bin/uniflow-ipp-server"

    install -Dm644 config.toml "$pkgdir/etc/uniflow-ipp/config.toml"

    install -Dm644 ppd/uniflow-ipp.ppd "$pkgdir/usr/share/uniflow-ipp/ppd/uniflow-ipp.ppd"

    install -Dm755 scripts/install-cups-queue.sh "$pkgdir/usr/share/uniflow-ipp/scripts/install-cups-queue.sh"

    install -Dm644 contrib/systemd/uniflow-ipp.service "$pkgdir/usr/lib/systemd/system/uniflow-ipp.service"

    install -d "$pkgdir/var/spool/uniflow-ipp"
}
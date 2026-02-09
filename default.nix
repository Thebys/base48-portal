{ pkgs ? import <nixpkgs> { } }:

let
  lib = pkgs.lib;
in
pkgs.buildGoModule rec {
  pname = "base48-portal";
  version = "1.0.48";
  src = ./.;

  vendorHash = "sha256-IVv6aQMOIR8zil9AdMSekAfFVkFV/MD2mrPZoatGkqQ=";

  buildPhase = ''
    runHook preBuild

    mkdir -p $out/bin
    mkdir -p $out/share/portal/web
    mkdir -p $out/share/portal/static

    export CGO_ENABLED=0
    export GOFLAGS="-p=$NIX_BUILD_CORES -trimpath -buildvcs=false"

    BUILD_DATE="${version} ($(date -u '+%Y-%m-%d %H:%M UTC'))"
    go build -ldflags="-s -w -X 'main.BuildDate=$BUILD_DATE'" -o $out/bin/portal cmd/server/main.go
    go build -ldflags="-s -w" -o $out/bin/sync_fio_payments cmd/cron/sync_fio_payments.go

    cp -r web/templates $out/share/portal/web/
    cp -r web/static $out/share/portal/web/

    runHook postBuild
  '';

  installPhase = "true";
  doCheck = false;
}

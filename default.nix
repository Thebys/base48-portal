{ pkgs ? import <nixpkgs> { } }:

let
  lib = pkgs.lib;
in
pkgs.buildGoModule rec {
  pname = "base48-portal";
  version = "0.1.0";
  src = ./.;

  vendorHash = "sha256-IVv6aQMOIR8zil9AdMSekAfFVkFV/MD2mrPZoatGkqQ=";

  nativeBuildInputs = [ pkgs.pkg-config ];
  buildInputs = [ pkgs.sqlite ];

  buildPhase = ''
    runHook preBuild
    mkdir -p $out/bin
    mkdir -p $out/share/portal/web

    go build -ldflags="-s -w" -o $out/bin/portal cmd/server/main.go

    go build -o $out/bin/sync_fio_payments cmd/cron/sync_fio_payments.go
    go build -o $out/bin/update_debt_status cmd/cron/update_debt_status.go

    cp -r web/templates $out/share/portal/web/

    runHook postBuild
  '';

  installPhase = "true";
  doCheck = false;
}

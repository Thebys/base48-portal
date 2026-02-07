{
	description = "The Base48's Memberportal";

	inputs = {
		# Release inputs
			nixpkgs-master.url = "github:nixos/nixpkgs/master";
			nixpkgs-staging-next.url = "github:nixos/nixpkgs/staging-next";
			nixpkgs-staging.url = "github:nixos/nixpkgs/staging";
			nixpkgs-unstable.url = "github:nixos/nixpkgs/nixos-unstable";

			nixpkgs.url = "github:nixos/nixpkgs/nixos-25.11";
			# nixpkgs.url = "git+file:///nix/persist/NiXium/vendor/nixpkgs-stable";

			nixpkgs-25_11.url = "github:nixos/nixpkgs/nixos-25.11";
			nixpkgs-25_05.url = "github:nixos/nixpkgs/nixos-25.05";
			nixpkgs-24_11.url = "github:nixos/nixpkgs/nixos-24.11";
			nixpkgs-24_05.url = "github:nixos/nixpkgs/nixos-24.05";
			nixpkgs-23_11.url = "github:nixos/nixpkgs/nixos-23.11";
			nixpkgs-23_05.url = "github:nixos/nixpkgs/nixos-23.05";
			nixpkgs-22_11.url = "github:nixos/nixpkgs/nixos-22.11";
			nixpkgs-22_05.url = "github:nixos/nixpkgs/nixos-22.05";

		# Principle inputs
			nixos-flake.url = "github:srid/nixos-flake";
			flake-parts.url = "github:hercules-ci/flake-parts";
			mission-control.url = "github:Platonic-Systems/mission-control";
			flake-root.url = "github:srid/flake-root";

		nixos-generators = {
			url = "github:nix-community/nixos-generators";
			inputs.nixpkgs.follows = "nixpkgs";
		};
		nixos-generators-unstable = {
			url = "github:nix-community/nixos-generators";
			inputs.nixpkgs.follows = "nixpkgs-unstable";
		};
		nixos-generators-master = {
			url = "github:nix-community/nixos-generators";
			inputs.nixpkgs.follows = "nixpkgs-master";
		};
	};

  outputs = inputs @ { self, ... }:
		inputs.flake-parts.lib.mkFlake { inherit inputs; } {
			imports = [
				./tasks # Include Tasks

				inputs.flake-root.flakeModule
				inputs.mission-control.flakeModule
			];

			# Set Supported Systems
			systems = [
				"x86_64-linux"
				"aarch64-linux"
				"riscv64-linux"
				"armv7l-linux"
			];

			perSystem = { system, config, inputs', ... }: {
				devShells.default = inputs.nixpkgs.legacyPackages.${system}.mkShell {
					name = "base48-devshell";
					nativeBuildInputs = let
						basePortal = inputs'.nixpkgs.legacyPackages.buildGoModule {
							pname = "base48-portal";
							version = "0.1.0";
							src = self.outPath;

							vendorHash = "sha256-IVv6aQMOIR8zil9AdMSekAfFVkFV/MD2mrPZoatGkqQ=";

							buildPhase = builtins.concatStringsSep "\n" [
								''runHook preBuild''

								''mkdir -pv "$out/bin"''
								''mkdir -pv "$out/share/portal/web"''
								''mkdir -pv "$out/share/portal/static"''

								''export CGO_ENABLED=0''
								''export GOFLAGS="-p=$NIX_BUILD_CORES -trimpath -buildvcs=false"''

								''go build -ldflags="-s -w" -o "$out/bin/portal" cmd/server/main.go''
								''go build -ldflags="-s -w" -o "$out/bin/sync_fio_payments" cmd/cron/sync_fio_payments.go''
								''go build -ldflags="-s -w" -o "$out/bin/update_debt_status" cmd/cron/update_debt_status.go''

								''cp -rv web/templates "$out/share/portal/web/"''
								''cp -rv web/static "$out/share/portal/web/"''

								''runHook postBuild''
							];

							installPhase = "true";
							doCheck = false;
						};
					in [
						# Project
						basePortal

						# Shell
						inputs.nixpkgs.legacyPackages.${system}.ksh # For Scripting
						inputs.nixpkgs.legacyPackages.${system}.bashInteractive # For terminal
						inputs.nixpkgs.legacyPackages.${system}.shellcheck # Linting of shell files

						# Nix
						inputs.nixpkgs.legacyPackages.${system}.nil # Needed for linting
						inputs.nixpkgs.legacyPackages.${system}.nixpkgs-fmt # Nixpkgs formatter


						# Benchmarks
						inputs.nixpkgs.legacyPackages.${system}.perf

						# Utilities
						inputs.nixpkgs.legacyPackages.${system}.fira-code # For liquratures in code editors
            inputs.nixpkgs.legacyPackages.${system}.git # Working with the codebase
						inputs.nixpkgs.legacyPackages.${system}.nano # Editor to work with the codebase in cli

						inputs.nixos-generators.packages.${system}.nixos-generate

						inputs.nixpkgs.legacyPackages.${system}.ungoogled-chromium # Web browser used in the integrated developer environment for interacting with the outside resources
					];
					inputsFrom = [
						config.mission-control.devShell
						config.flake-root.devShell
					];
					# Environmental Variables
					#VARIABLE = "value"; # Comment
				};

				formatter = inputs.nixpkgs.legacyPackages.${system}.nixpkgs-fmt;
			};
		};
}

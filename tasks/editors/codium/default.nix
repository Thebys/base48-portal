{ self, ... }:

{
	perSystem = { inputs', lib, pkgs, ... }: {
		mission-control.scripts = {
			codium = {
				description = "Open repository-standardized VSCodium version with configuration";
				category = "Integrated Development Environments";
				exec = ''${inputs'.nixpkgs.legacyPackages.vscodium}/bin/codium "${self.outPath}/default.code-workspace"'';
			};
		};
	};
}

// Generates npm/dist/<scoped-pkg>/ for each platform from the release archives in
// dist/. Reads the goreleaser archives (stable names we control via name_template),
// not the build directories (whose names include GOAMD64/GOARM64 suffixes).
// Usage: node npm/build-platform-packages.mjs <version>
import { mkdirSync, writeFileSync, existsSync, chmodSync } from "node:fs";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

const version = process.argv[2];
if (!version) {
  console.error("usage: node npm/build-platform-packages.mjs <version>");
  process.exit(1);
}

// [npmPlatform, npmArch, archiveFile, exe, isZip]
const targets = [
  ["darwin", "arm64", "tunnel_darwin_arm64.tar.gz", "tunnel", false],
  ["darwin", "x64", "tunnel_darwin_amd64.tar.gz", "tunnel", false],
  ["linux", "x64", "tunnel_linux_amd64.tar.gz", "tunnel", false],
  ["linux", "arm64", "tunnel_linux_arm64.tar.gz", "tunnel", false],
  ["win32", "x64", "tunnel_windows_amd64.zip", "tunnel.exe", true],
  ["win32", "arm64", "tunnel_windows_arm64.zip", "tunnel.exe", true],
];

for (const [os, arch, archiveFile, exe, isZip] of targets) {
  const pkgName = `@ur-link/tunnel-${os}-${arch}`;
  const outDir = join("npm", "dist", pkgName); // mkdir -p handles the @scope/ segment
  const binDir = join(outDir, "bin");
  mkdirSync(binDir, { recursive: true });

  const archive = join("dist", archiveFile);
  if (!existsSync(archive)) {
    console.error(`missing archive: ${archive}`);
    process.exit(1);
  }
  // Extract just the binary, flattened into binDir.
  if (isZip) {
    execFileSync("unzip", ["-o", "-j", archive, exe, "-d", binDir], { stdio: "inherit" });
  } else {
    execFileSync("tar", ["-xzf", archive, "-C", binDir, exe], { stdio: "inherit" });
  }
  chmodSync(join(binDir, exe), 0o755);

  const pkg = {
    name: pkgName,
    version,
    description: `Prebuilt tunnel binary for ${os}-${arch}.`,
    os: [os],
    cpu: [arch],
    license: "MIT",
    repository: {
      type: "git",
      url: "git+https://github.com/ur-link/tunnel.git",
    },
    files: [`bin/${exe}`],
  };
  writeFileSync(join(outDir, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
  console.log(`prepared ${pkgName}@${version}`);
}

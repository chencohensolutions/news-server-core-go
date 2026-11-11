# Builds news-server.exe here with the packs listed in packs.txt (see packs.example.txt); without packs.txt, the
# plain server. Extra arguments go to newsbuild: -o <file> to write the program elsewhere, --with <pack> instead of
# packs.txt. Run it again after pulling the core or a pack.
Push-Location $PSScriptRoot
try {
    go run ./cmd/newsbuild @args
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}

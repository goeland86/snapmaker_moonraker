$env:GOOS = "linux"
$env:GOARCH = "arm"
$env:GOARM = "7"
go build -o snapmaker-moonraker .

module mkres

go 1.24.0

toolchain go1.24.7

require (
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646
	github.com/tc-hib/winres v0.3.1
	golang.org/x/image v0.34.0
	golang.org/x/text v0.32.0
)

replace (
	github.com/nfnt/resize => ../../third_party/nfnt-resize
	github.com/tc-hib/winres => ../../third_party/winres
	golang.org/x/image => ../../third_party/x-image
	golang.org/x/text => ../../third_party/x-text
)

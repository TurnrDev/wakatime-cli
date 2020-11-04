package filestats

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/wakatime/wakatime-cli/pkg/heartbeat"

	jww "github.com/spf13/jwalterweatherman"
)

// Max file size supporting line number count stats. Files larger than this in
// bytes will not have a line count stat for performance. Default is 2MB (2*1024*1014).
const maxFileSizeSupported = 2097152

// WithDetection initializes and returns a heartbeat handle option, which
// can be used in a heartbeat processing pipeline to detect filestats. At the
// moment only the total number of lines in a file is detected.
func WithDetection() heartbeat.HandleOption {
	return func(next heartbeat.Handle) heartbeat.Handle {
		return func(hh []heartbeat.Heartbeat) ([]heartbeat.Result, error) {
			for n, h := range hh {
				if h.EntityType == heartbeat.FileType {
					filepath := h.Entity
					if h.LocalFile != "" {
						filepath = h.LocalFile
					}

					fileInfo, err := os.Stat(filepath)
					if err != nil {
						jww.WARN.Printf("failed to retrieve file stats of file %q: %s", filepath, err)
						continue
					}

					if fileInfo.Size() > maxFileSizeSupported {
						jww.DEBUG.Printf(
							"file %q exceeds max file size of %d bytes. Lines won't be counted",
							h.Entity,
							maxFileSizeSupported,
						)

						continue
					}

					lines, err := countLineNumbers(filepath)
					if err != nil {
						jww.WARN.Printf("failed to detect the total number of lines in file %q: %s", filepath, err)
						continue
					}

					hh[n].Lines = heartbeat.Int(lines)
				}
			}

			return next(hh)
		}
	}
}

func countLineNumbers(filepath string) (int, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return 0, fmt.Errorf("failed to open file: %s", err)
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	count := 0
	lineSep := []byte{'\n'}

	for {
		c, err := f.Read(buf)
		count += bytes.Count(buf[:c], lineSep)

		switch {
		case err == io.EOF:
			return count, nil

		case err != nil:
			return count, err
		}
	}
}
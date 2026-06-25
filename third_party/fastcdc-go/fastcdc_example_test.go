package fastcdc_test

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"io"
	"log"

	fastcdc "github.com/markgustetic/sentra/third_party/fastcdc-go"
)

func Example_basic() {

	data := make([]byte, 10*1024*1024)
	for i := range data {
		data[i] = byte((i*31 + i/251) % 256)
	}
	rd := bytes.NewReader(data)

	chunker, err := fastcdc.NewChunker(rd, fastcdc.Options{
		AverageSize: 1024 * 1024, // target 1 MiB average chunk size
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%-32s  %s\n", "CHECKSUM", "CHUNK SIZE")

	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("%x  %d\n", md5.Sum(chunk.Data), chunk.Length)
	}

	// Output:
	// CHECKSUM                          CHUNK SIZE
	// 12034d718be6991f461e4b6949885434  4194304
	// e1574ccf547cceb2836095f19a1a375d  4194304
	// 8e8c270dac0ced3b98d7b7d60ae05064  2097152
}

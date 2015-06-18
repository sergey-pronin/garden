package connection

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/cloudfoundry-incubator/garden/transport"
	"github.com/pivotal-golang/lager"
)

type hijackFunc func(streamID uint32, streamType string) (net.Conn, io.Reader, error)

type streamHandler struct {
	conn            *connection
	containerHandle string
	processPipeline *processStream
	streamID        uint32
	hijack          hijackFunc
	wg              *sync.WaitGroup
}

func newStreamHandler(processPipeline *processStream, conn *connection, handle string, streamID uint32, hijack hijackFunc) *streamHandler {
	return &streamHandler{
		conn:            conn,
		containerHandle: handle,
		processPipeline: processPipeline,
		streamID:        streamID,
		wg:              new(sync.WaitGroup),
		hijack:          hijack,
	}
}

func (sh *streamHandler) streamIn(stdin io.Reader) {
	if stdin == nil {
		return
	}

	go func(processInputStream *processStream, stdin io.Reader, log lager.Logger) {
		processInputStreamWriter := &stdinWriter{processInputStream}
		if _, err := io.Copy(processInputStreamWriter, stdin); err == nil {
			processInputStreamWriter.Close()
		} else {
			log.Error("streaming-stdin-payload", err)
		}
	}(sh.processPipeline, stdin, sh.conn.log)
}

func (sh *streamHandler) streamOut(streamType string, streamWriter io.Writer) error {
	if streamWriter == nil {
		return nil
	}

	if stdout, err := sh.attach(streamType); err != nil {
		err := fmt.Errorf("connection: attach to stream %s: %s", streamType, err)
		sh.conn.log.Error("attach-to-stream-failed", err)
		return err
	} else {
		go sh.copyStream(streamWriter, stdout)
	}

	return nil
}

// attaches to the given standard stream endpoint for a running process
// and copies output to a local io.writer
func (sh *streamHandler) attach(streamType string) (io.Reader, error) {
	source, err := sh.connect(streamType)
	if err != nil {
		return nil, err
	}

	sh.wg.Add(1)
	return source, nil
}

func (sh *streamHandler) connect(route string) (io.Reader, error) {
	_, source, err := sh.hijack(sh.streamID, route)

	if err != nil {
		return nil, fmt.Errorf("Failed to hijack stream %s: %s", route, err)
	}

	return source, nil
}

func (sh *streamHandler) copyStream(target io.Writer, source io.Reader) {
	io.Copy(target, source)
	sh.wg.Done()
}

func (sh *streamHandler) wait(decoder *json.Decoder) (int, error) {
	for {
		payload := &transport.ProcessPayload{}
		err := decoder.Decode(payload)
		if err != nil {
			sh.wg.Wait()
			return 0, fmt.Errorf("connection: decode failed: %s", err)
		}

		if payload.Error != nil {
			sh.wg.Wait()
			return 0, fmt.Errorf("connection: process error: %s", *payload.Error)
		}

		if payload.ExitStatus != nil {
			sh.wg.Wait()
			status := int(*payload.ExitStatus)
			return status, nil
		}

		// discard other payloads
	}
}

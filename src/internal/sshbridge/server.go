package sshbridge

import (
	"context"
	"errors"
	"fmt"
	"io"

	"remote-session-runner/src/internal/domain"
)

// ServerOptions configures the protocol-only bridge server. The authenticated
// key map is mandatory; no request field can supply or override a controller.
type ServerOptions struct {
	Controllers   KeyControllerMap
	Handler       RequestHandler
	MaxFrameBytes int
}

// Server owns framing, handshake, and authentication mapping. It contains no
// shell/process execution logic and does not forward requests in P049 unless a
// later phase supplies an explicit handler.
type Server struct {
	controllers KeyControllerMap
	handler     RequestHandler
	maxBytes    int
}

// NewServer validates and copies the server-side controller mapping.
func NewServer(options ServerOptions) (*Server, error) {
	controllers, err := NewKeyControllerMap(options.Controllers)
	if err != nil {
		return nil, err
	}
	if len(controllers) == 0 {
		return nil, fmt.Errorf("%w: no key mappings", ErrUnknownAuthenticatedKey)
	}
	if options.MaxFrameBytes <= 0 {
		options.MaxFrameBytes = domain.MaxSerializedFrameBytes
	}
	return &Server{controllers: controllers, handler: options.Handler, maxBytes: options.MaxFrameBytes}, nil
}

// Serve processes newline-delimited requests until EOF or context cancellation.
// hello and ping are answered locally; installed handlers forward the remaining
// operations, including multi-frame event streams.
func (s *Server) Serve(ctx context.Context, authenticatedKey string, reader io.Reader, writer io.Writer) error {
	if s == nil || reader == nil || writer == nil {
		return ErrInvalidFrame
	}
	controller, err := s.controllers.ControllerForKey(authenticatedKey)
	if err != nil {
		return err
	}
	decoder := NewDecoderWithLimit(reader, s.maxBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var reply ReplyFrame
		switch request.Operation {
		case OperationHello:
			reply = helloReply(request.RequestID)
		case OperationPing:
			reply = pingReply(request.RequestID)
		default:
			if s.handler == nil {
				reply = errorReply(request.RequestID, fmt.Errorf("%w: %s", ErrOperationUnsupported, request.Operation))
			} else if request.Operation == OperationStreamCommandEvents {
				streamer, ok := s.handler.(StreamRequestHandler)
				if !ok {
					reply = errorReply(request.RequestID, fmt.Errorf("%w: %s", ErrOperationUnsupported, request.Operation))
					if err := Encode(writer, reply); err != nil {
						return err
					}
					continue
				}
				wrote := false
				err := streamer.Stream(ctx, controller, request, func(streamReply ReplyFrame) error {
					if err := Encode(writer, streamReply); err != nil {
						return err
					}
					wrote = true
					return nil
				})
				if err != nil {
					if wrote {
						return err
					}
					reply = errorReply(request.RequestID, err)
				} else {
					continue
				}
			} else {
				reply, err = s.handler.Handle(ctx, controller, request)
				if err != nil {
					reply = errorReply(request.RequestID, err)
				}
			}
		}
		if err := Encode(writer, reply); err != nil {
			return err
		}
	}
}

// ControllerForKey exposes the resolved principal for adapters and tests while
// retaining the mapping's server-side ownership.
func (s *Server) ControllerForKey(authenticatedKey string) (domain.ControllerIdentity, error) {
	if s == nil {
		return domain.ControllerIdentity{}, ErrUnknownAuthenticatedKey
	}
	return s.controllers.ControllerForKey(authenticatedKey)
}

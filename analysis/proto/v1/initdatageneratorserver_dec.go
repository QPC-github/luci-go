// Code generated by svcdec; DO NOT EDIT.

package analysispb

import (
	"context"

	proto "github.com/golang/protobuf/proto"
)

type DecoratedInitDataGenerator struct {
	// Service is the service to decorate.
	Service InitDataGeneratorServer
	// Prelude is called for each method before forwarding the call to Service.
	// If Prelude returns an error, then the call is skipped and the error is
	// processed via the Postlude (if one is defined), or it is returned directly.
	Prelude func(ctx context.Context, methodName string, req proto.Message) (context.Context, error)
	// Postlude is called for each method after Service has processed the call, or
	// after the Prelude has returned an error. This takes the Service's
	// response proto (which may be nil) and/or any error. The decorated
	// service will return the response (possibly mutated) and error that Postlude
	// returns.
	Postlude func(ctx context.Context, methodName string, rsp proto.Message, err error) error
}

func (s *DecoratedInitDataGenerator) GenerateInitData(ctx context.Context, req *GenerateInitDataRequest) (rsp *GenerateInitDataResponse, err error) {
	if s.Prelude != nil {
		var newCtx context.Context
		newCtx, err = s.Prelude(ctx, "GenerateInitData", req)
		if err == nil {
			ctx = newCtx
		}
	}
	if err == nil {
		rsp, err = s.Service.GenerateInitData(ctx, req)
	}
	if s.Postlude != nil {
		err = s.Postlude(ctx, "GenerateInitData", rsp, err)
	}
	return
}

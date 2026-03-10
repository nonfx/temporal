package nexusoperation

import (
	"context"
	"errors"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/chasm"
	nexusoperationpb "go.temporal.io/server/chasm/lib/nexusoperation/gen/nexusoperationpb/v1"
	"go.temporal.io/server/common/contextutil"
	"go.temporal.io/server/common/log"
)

type handler struct {
	nexusoperationpb.UnimplementedNexusOperationServiceServer

	config *Config
	logger log.Logger
}

func newHandler(config *Config, logger log.Logger) *handler {
	return &handler{
		config: config,
		logger: logger,
	}
}

// DescribeNexusOperationExecution queries current operation state, optionally as a long-poll that
// waits for any state change. When used to long-poll, it returns an empty non-error response on
// context deadline expiry, to indicate that the state being waited for was not reached.
func (h *handler) DescribeNexusOperationExecution(
	ctx context.Context,
	req *nexusoperationpb.DescribeNexusOperationExecutionRequest,
) (response *nexusoperationpb.DescribeNexusOperationExecutionResponse, err error) {
	defer log.CapturePanic(h.logger, &err)

	ref := chasm.NewComponentRef[*Operation](chasm.ExecutionKey{
		NamespaceID: req.GetNamespaceId(),
		BusinessID:  req.GetFrontendRequest().GetOperationId(),
		RunID:       req.GetFrontendRequest().GetRunId(),
	})

	ns := req.GetFrontendRequest().GetNamespace()
	ctx, cancel := contextutil.WithDeadlineBuffer(
		ctx,
		h.config.LongPollTimeout(ns),
		h.config.LongPollBuffer(ns),
	)
	defer cancel()

	token := req.GetFrontendRequest().GetLongPollToken()
	if len(token) == 0 {
		return chasm.ReadComponent(ctx, ref, (*Operation).buildDescribeResponse, req, nil)
	}

	response, _, err = chasm.PollComponent(ctx, ref, func(
		op *Operation,
		ctx chasm.Context,
		req *nexusoperationpb.DescribeNexusOperationExecutionRequest,
	) (*nexusoperationpb.DescribeNexusOperationExecutionResponse, bool, error) {
		changed, err := chasm.ExecutionStateChanged(op, ctx, token)
		if err != nil {
			if errors.Is(err, chasm.ErrMalformedComponentRef) {
				return nil, false, serviceerror.NewInvalidArgument("invalid long poll token")
			}
			if errors.Is(err, chasm.ErrInvalidComponentRef) {
				return nil, false, serviceerror.NewInvalidArgument("long poll token does not match execution")
			}
			return nil, false, err
		}
		if changed {
			response, err := op.buildDescribeResponse(ctx, req)
			return response, true, err
		}
		return nil, false, nil
	}, req)

	if err != nil && ctx.Err() != nil {
		return &nexusoperationpb.DescribeNexusOperationExecutionResponse{
			FrontendResponse: &workflowservice.DescribeNexusOperationExecutionResponse{},
		}, nil
	}
	return response, err
}

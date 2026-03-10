package nexusoperation

import (
	"context"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/chasm"
	nexusoperationpb "go.temporal.io/server/chasm/lib/nexusoperation/gen/nexusoperationpb/v1"
	"go.temporal.io/server/common/log"
	"go.temporal.io/server/common/namespace"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FrontendHandler provides the frontend-facing API for standalone Nexus operations.
type FrontendHandler interface {
	DescribeNexusOperationExecution(context.Context, *workflowservice.DescribeNexusOperationExecutionRequest) (*workflowservice.DescribeNexusOperationExecutionResponse, error)
	ListNexusOperationExecutions(context.Context, *workflowservice.ListNexusOperationExecutionsRequest) (*workflowservice.ListNexusOperationExecutionsResponse, error)
	IsStandaloneNexusOperationEnabled(namespaceName string) bool
}

var ErrStandaloneNexusOperationDisabled = serviceerror.NewUnimplemented("Standalone Nexus operation is disabled")

type frontendHandler struct {
	client            nexusoperationpb.NexusOperationServiceClient
	config            *Config
	logger            log.Logger
	namespaceRegistry namespace.Registry
}

func NewFrontendHandler(
	client nexusoperationpb.NexusOperationServiceClient,
	config *Config,
	logger log.Logger,
	namespaceRegistry namespace.Registry,
) FrontendHandler {
	return &frontendHandler{
		client:            client,
		config:            config,
		logger:            logger,
		namespaceRegistry: namespaceRegistry,
	}
}

func (h *frontendHandler) IsStandaloneNexusOperationEnabled(namespaceName string) bool {
	return h.config.ChasmNexusEnabled()
}

func (h *frontendHandler) DescribeNexusOperationExecution(
	ctx context.Context,
	req *workflowservice.DescribeNexusOperationExecutionRequest,
) (*workflowservice.DescribeNexusOperationExecutionResponse, error) {
	if !h.IsStandaloneNexusOperationEnabled(req.GetNamespace()) {
		return nil, ErrStandaloneNexusOperationDisabled
	}

	if err := validateDescribeRequest(req, h.config.MaxIDLengthLimit()); err != nil {
		return nil, err
	}

	namespaceID, err := h.namespaceRegistry.GetNamespaceID(namespace.Name(req.GetNamespace()))
	if err != nil {
		return nil, err
	}

	resp, err := h.client.DescribeNexusOperationExecution(ctx, &nexusoperationpb.DescribeNexusOperationExecutionRequest{
		NamespaceId:     namespaceID.String(),
		FrontendRequest: req,
	})
	return resp.GetFrontendResponse(), err
}

func (h *frontendHandler) ListNexusOperationExecutions(
	ctx context.Context,
	req *workflowservice.ListNexusOperationExecutionsRequest,
) (*workflowservice.ListNexusOperationExecutionsResponse, error) {
	if !h.IsStandaloneNexusOperationEnabled(req.GetNamespace()) {
		return nil, ErrStandaloneNexusOperationDisabled
	}

	pageSize := req.GetPageSize()
	if maxPageSize := int32(h.config.VisibilityMaxPageSize(req.GetNamespace())); pageSize <= 0 || pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	namespaceID, err := h.namespaceRegistry.GetNamespaceID(namespace.Name(req.GetNamespace()))
	if err != nil {
		return nil, err
	}

	resp, err := chasm.ListExecutions[*Operation, *emptypb.Empty](ctx, &chasm.ListExecutionsRequest{
		NamespaceID:   namespaceID.String(),
		NamespaceName: req.GetNamespace(),
		PageSize:      int(pageSize),
		NextPageToken: req.GetNextPageToken(),
		Query:         req.GetQuery(),
	})
	if err != nil {
		return nil, err
	}

	operations := make([]*nexuspb.NexusOperationExecutionListInfo, 0, len(resp.Executions))
	for _, exec := range resp.Executions {
		endpoint, _ := chasm.SearchAttributeValue(exec.ChasmSearchAttributes, EndpointSearchAttribute)
		service, _ := chasm.SearchAttributeValue(exec.ChasmSearchAttributes, ServiceSearchAttribute)
		operation, _ := chasm.SearchAttributeValue(exec.ChasmSearchAttributes, OperationSearchAttribute)
		statusStr, _ := chasm.SearchAttributeValue(exec.ChasmSearchAttributes, StatusSearchAttribute)
		status, _ := enumspb.NexusOperationExecutionStatusFromString(statusStr)

		info := &nexuspb.NexusOperationExecutionListInfo{
			OperationId:          exec.BusinessID,
			RunId:                exec.RunID,
			Endpoint:             endpoint,
			Service:              service,
			Operation:            operation,
			ScheduleTime:         timestamppb.New(exec.StartTime),
			Status:               status,
			StateTransitionCount: exec.StateTransitionCount,
			StateSizeBytes:       exec.HistorySizeBytes,
			SearchAttributes:     &commonpb.SearchAttributes{IndexedFields: exec.CustomSearchAttributes},
		}
		if !exec.CloseTime.IsZero() {
			info.CloseTime = timestamppb.New(exec.CloseTime)
			if !exec.StartTime.IsZero() {
				info.ExecutionDuration = durationpb.New(exec.CloseTime.Sub(exec.StartTime))
			}
		}
		operations = append(operations, info)
	}

	return &workflowservice.ListNexusOperationExecutionsResponse{
		Operations:    operations,
		NextPageToken: resp.NextPageToken,
	}, nil
}

func validateDescribeRequest(req *workflowservice.DescribeNexusOperationExecutionRequest, maxIDLengthLimit int) error {
	if req.GetNamespace() == "" {
		return serviceerror.NewInvalidArgument("namespace is required")
	}
	if req.GetOperationId() == "" {
		return serviceerror.NewInvalidArgument("operation_id is required")
	}
	if len(req.GetOperationId()) > maxIDLengthLimit {
		return serviceerror.NewInvalidArgumentf("operation_id exceeds length limit. Length=%d Limit=%d",
			len(req.GetOperationId()), maxIDLengthLimit)
	}
	if len(req.GetRunId()) > maxIDLengthLimit {
		return serviceerror.NewInvalidArgumentf("run_id exceeds length limit. Length=%d Limit=%d",
			len(req.GetRunId()), maxIDLengthLimit)
	}
	if len(req.GetLongPollToken()) > 0 && req.GetRunId() == "" {
		return serviceerror.NewInvalidArgument("run_id is required when long_poll_token is set")
	}
	return nil
}

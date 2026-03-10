package nexusoperation

import (
	enumspb "go.temporal.io/api/enums/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/server/chasm"
	nexusoperationpb "go.temporal.io/server/chasm/lib/nexusoperation/gen/nexusoperationpb/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (o *Operation) buildDescribeResponse(
	ctx chasm.Context,
	req *nexusoperationpb.DescribeNexusOperationExecutionRequest,
) (*nexusoperationpb.DescribeNexusOperationExecutionResponse, error) {
	frontendReq := req.GetFrontendRequest()

	token, err := ctx.Ref(o)
	if err != nil {
		return nil, err
	}

	info := o.buildExecutionInfo(ctx)

	response := &workflowservice.DescribeNexusOperationExecutionResponse{
		RunId:         ctx.ExecutionKey().RunID,
		Info:          info,
		LongPollToken: token,
	}

	// TODO: populate input from stored request data when available.
	_ = frontendReq.GetIncludeInput()

	// TODO: populate outcome from stored result/failure when available.
	_ = frontendReq.GetIncludeOutcome()

	return &nexusoperationpb.DescribeNexusOperationExecutionResponse{
		FrontendResponse: response,
	}, nil
}

func (o *Operation) buildExecutionInfo(ctx chasm.Context) *nexuspb.NexusOperationExecutionInfo {
	info := &nexuspb.NexusOperationExecutionInfo{
		Endpoint:                o.Endpoint,
		Service:                 o.Service,
		Operation:               o.Operation,
		Status:                  operationExecutionStatus(o.Status),
		State:                   pendingOperationState(o.Status),
		ScheduleToCloseTimeout:  o.ScheduleToCloseTimeout,
		ScheduleToStartTimeout:  o.ScheduleToStartTimeout,
		StartToCloseTimeout:     o.StartToCloseTimeout,
		Attempt:                 o.Attempt,
		ScheduleTime:            o.ScheduledTime,
		LastAttemptCompleteTime: o.LastAttemptCompleteTime,
		LastAttemptFailure:      o.LastAttemptFailure,
		NextAttemptScheduleTime: o.NextAttemptScheduleTime,
		RequestId:               o.RequestId,
		OperationToken:          o.OperationToken,
	}

	if o.ScheduledTime != nil && o.ScheduleToCloseTimeout != nil {
		info.ExpirationTime = timestamppb.New(
			o.ScheduledTime.AsTime().Add(o.ScheduleToCloseTimeout.AsDuration()),
		)
	}

	now := ctx.Now(o)
	if o.ScheduledTime != nil {
		if o.LifecycleState(ctx).IsClosed() {
			if closeTime := o.closeTime(); closeTime != nil {
				info.CloseTime = closeTime
				info.ExecutionDuration = durationpb.New(closeTime.AsTime().Sub(o.ScheduledTime.AsTime()))
			}
		} else {
			info.ExecutionDuration = durationpb.New(now.Sub(o.ScheduledTime.AsTime()))
		}
	}

	return info
}

// closeTime returns the close time of the operation based on LastAttemptCompleteTime for terminal states.
func (o *Operation) closeTime() *timestamppb.Timestamp {
	switch o.Status {
	case nexusoperationpb.OPERATION_STATUS_SUCCEEDED,
		nexusoperationpb.OPERATION_STATUS_FAILED,
		nexusoperationpb.OPERATION_STATUS_CANCELED,
		nexusoperationpb.OPERATION_STATUS_TIMED_OUT:
		return o.LastAttemptCompleteTime
	default:
		return nil
	}
}

func operationExecutionStatus(status nexusoperationpb.OperationStatus) enumspb.NexusOperationExecutionStatus {
	switch status {
	case nexusoperationpb.OPERATION_STATUS_SCHEDULED,
		nexusoperationpb.OPERATION_STATUS_BACKING_OFF,
		nexusoperationpb.OPERATION_STATUS_STARTED:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_RUNNING
	case nexusoperationpb.OPERATION_STATUS_SUCCEEDED:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_COMPLETED
	case nexusoperationpb.OPERATION_STATUS_FAILED:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_FAILED
	case nexusoperationpb.OPERATION_STATUS_CANCELED:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_CANCELED
	case nexusoperationpb.OPERATION_STATUS_TIMED_OUT:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_TIMED_OUT
	default:
		return enumspb.NEXUS_OPERATION_EXECUTION_STATUS_UNSPECIFIED
	}
}

func pendingOperationState(status nexusoperationpb.OperationStatus) enumspb.PendingNexusOperationState {
	switch status {
	case nexusoperationpb.OPERATION_STATUS_SCHEDULED:
		return enumspb.PENDING_NEXUS_OPERATION_STATE_SCHEDULED
	case nexusoperationpb.OPERATION_STATUS_BACKING_OFF:
		return enumspb.PENDING_NEXUS_OPERATION_STATE_BACKING_OFF
	case nexusoperationpb.OPERATION_STATUS_STARTED:
		return enumspb.PENDING_NEXUS_OPERATION_STATE_STARTED
	default:
		return enumspb.PENDING_NEXUS_OPERATION_STATE_UNSPECIFIED
	}
}

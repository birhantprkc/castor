package cast

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/proto"

	castorv1 "github.com/stupside/castor/gen/castor/v1"
)

// announced is what the command says as a cast enters each phase it names.
var announced = map[castorv1.Phase]string{
	castorv1.Phase_PHASE_CONNECTING: "cast connecting the device",
	castorv1.Phase_PHASE_EXTRACTING: "cast finding streams",
}

// changed logs what moved between two statuses of a cast.
func changed(ctx context.Context, last, now *castorv1.CastStatus) {
	if msg, ok := announced[now.GetPhase()]; ok && last.GetPhase() != now.GetPhase() {
		slog.InfoContext(ctx, msg)
	}
	if now.GetStreams() != last.GetStreams() {
		slog.InfoContext(ctx, "cast measuring", "streams", now.GetStreams())
	}
	if now.GetCastable() != last.GetCastable() {
		slog.InfoContext(ctx, "cast measured", "castable", now.GetCastable())
	}
	if !proto.Equal(now.GetRevision(), last.GetRevision()) {
		slog.WarnContext(ctx, "cast revising", "strategy", now.GetRevision().GetStrategy(), "why", now.GetRevision().GetWhy())
	}
	if now.GetAttempt() != last.GetAttempt() {
		slog.InfoContext(ctx, "cast attempting", "try", now.GetAttempt())
	}
}

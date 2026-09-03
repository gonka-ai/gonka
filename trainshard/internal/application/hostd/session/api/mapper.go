package api

import (
	"time"

	usecases "trainshard/internal/application/hostd/session/use_cases"
	"trainshard/internal/contract"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

var errSince = shared.New("VALIDATION_ERROR", shared.ErrValidation, "since is not a timestamp")

func toSessionCommand(participant vo.Participant, actor shard.Actor, path, nodeID string) (usecases.SessionCommand, error) {
	shardID, err := vo.ParseShardID(path)
	if err != nil {
		return usecases.SessionCommand{}, err
	}
	node, err := vo.ParseNodeRef(string(participant), nodeID)
	if err != nil {
		return usecases.SessionCommand{}, err
	}
	return usecases.SessionCommand{Shard: shardID, Node: node, Actor: actor}, nil
}

func toLogsCommand(participant vo.Participant, actor shard.Actor, path, nodeID string, dto contract.LogsRequest) (usecases.LogsCommand, error) {
	base, err := toSessionCommand(participant, actor, path, nodeID)
	if err != nil {
		return usecases.LogsCommand{}, err
	}

	cmd := usecases.LogsCommand{SessionCommand: base, Tail: dto.Tail}
	if dto.Since != "" {
		since, err := time.Parse(time.RFC3339, dto.Since)
		if err != nil {
			return usecases.LogsCommand{}, errSince
		}
		cmd.Since = since
	}
	return cmd, nil
}

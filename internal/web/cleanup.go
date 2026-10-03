package web

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Server) initCleanup() {
	s.DB.PurgeBlobs = func(ids []int64) {
		for _, id := range ids {
			s.Jobs.Enqueue("purge", func(ctx context.Context) error { return s.Storage.Purge(ctx, id) })
		}
	}
	s.DB.RemoveBannedContent = func(id int64) {
		s.Jobs.Enqueue("ban", func(ctx context.Context) error {
			messages, err := s.DB.MessagesByCreator(ctx, id)
			if err != nil {
				return err
			}
			for _, message := range messages {
				if err = s.DB.RemoveBannedMessage(ctx, message.ID); errors.Is(err, sql.ErrNoRows) {
					continue
				} else if err != nil {
					return err
				}
				s.publish(message.RoomID, stream("remove", "message_"+message.ClientID, ""))
			}
			return nil
		})
	}
}

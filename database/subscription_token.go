package database

import (
	"fmt"

	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/util/common"
	"gorm.io/gorm"
)

func backfillSubscriptionTokens(db *gorm.DB) error {
	err := db.Transaction(func(tx *gorm.DB) error {
		var lastID uint
		for {
			var ids []uint
			if err := tx.Model(&model.Client{}).Where("id > ? AND (sub_token IS NULL OR sub_token = '')", lastID).
				Order("id").Limit(500).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			for _, id := range ids {
				token, err := common.NewSubscriptionToken()
				if err != nil {
					return err
				}
				if err := tx.Model(&model.Client{}).Where("id = ?", id).UpdateColumn("sub_token", token).Error; err != nil {
					return err
				}
			}
			lastID = ids[len(ids)-1]
		}
	})
	if err != nil {
		return fmt.Errorf("backfill subscription tokens: %w", err)
	}
	return nil
}

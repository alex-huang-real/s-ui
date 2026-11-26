package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/util/common"
	"gorm.io/gorm"
)

type ClientSubscription struct {
	Name     string          `json:"name"`
	SubToken string          `json:"subToken"`
	Links    json.RawMessage `json:"links,omitempty"`
}

func (s *ClientService) GetSubscription(id uint) (ClientSubscription, error) {
	var client model.Client
	err := database.GetDB().Select("name", "sub_token", "links").First(&client, id).Error
	return ClientSubscription{Name: client.Name, SubToken: client.SubToken, Links: client.Links}, err
}

func (s *ClientService) ResetSubscriptionToken(id uint, actor string) (string, error) {
	token, err := common.NewSubscriptionToken()
	if err != nil {
		return "", err
	}
	err = database.GetDB().Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Client{}).Where("id = ?", id).UpdateColumn("sub_token", token)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(&model.Changes{
			DateTime: time.Now().Unix(),
			Actor:    actor,
			Key:      "clients",
			Action:   "resetSubToken",
			Obj:      json.RawMessage(fmt.Sprintf(`{"id":%d}`, id)),
		}).Error
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Strip the server-managed field from client objects before saving the audit
// payload. RawMessage keeps integers in the rest of the client data exact.
func stripSubscriptionToken(action string, data json.RawMessage) (json.RawMessage, error) {
	changed := false
	removeToken := func(client map[string]json.RawMessage) {
		for key := range client {
			if strings.EqualFold(key, "subToken") || strings.EqualFold(key, "sub_token") {
				delete(client, key)
				changed = true
			}
		}
	}
	switch action {
	case "new", "edit":
		var client map[string]json.RawMessage
		if err := json.Unmarshal(data, &client); err != nil {
			return nil, err
		}
		removeToken(client)
		if changed {
			return json.Marshal(client)
		}
	case "addbulk", "editbulk":
		var clients []map[string]json.RawMessage
		if err := json.Unmarshal(data, &clients); err != nil {
			return nil, err
		}
		for _, client := range clients {
			removeToken(client)
		}
		if changed {
			return json.Marshal(clients)
		}
	}
	return data, nil
}

package api

import (
	"strconv"

	"github.com/alireza0/s-ui/util/common"
	"github.com/gin-gonic/gin"
)

func subscriptionClientID(raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, strconv.IntSize-1)
	if err != nil || id == 0 {
		return 0, common.NewError("invalid client id")
	}
	return uint(id), nil
}

func (a *ApiService) GetClientSubscription(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := subscriptionClientID(c.Query("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	subscription, err := a.ClientService.GetSubscription(id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, subscription, nil)
}

func (a *ApiService) ResetSubToken(c *gin.Context, actor string) {
	c.Header("Cache-Control", "no-store")
	id, err := subscriptionClientID(c.PostForm("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	token, err := a.ClientService.ResetSubscriptionToken(id, actor)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonMsgObj(c, "resetSubToken", gin.H{"subToken": token}, nil)
}

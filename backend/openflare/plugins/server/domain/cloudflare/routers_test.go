// Copyright 2026 Arctel.net
// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Wavelet/openflare/plugins/server/kernel/repository"
	"Wavelet/pkg/response"

	"github.com/gin-gonic/gin"
)

func TestConnectionHandlersNeverReturnAPIToken(t *testing.T) {
	setupCloudflareLogicDB(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.PUT("/connection", SaveConnectionHandler)
	router.GET("/connection", GetConnectionHandler)

	save := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/connection", strings.NewReader(`{"source":"standalone","api_token":"top-secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(save, request)
	if save.Code != http.StatusOK {
		t.Fatalf("PUT /connection status = %d, body = %s", save.Code, save.Body.String())
	}
	if strings.Contains(save.Body.String(), "top-secret-token") || strings.Contains(save.Body.String(), "api_token") {
		t.Fatalf("PUT /connection leaked token: %s", save.Body.String())
	}

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/connection", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET /connection status = %d, body = %s", get.Code, get.Body.String())
	}
	if strings.Contains(get.Body.String(), "top-secret-token") || strings.Contains(get.Body.String(), "api_token") {
		t.Fatalf("GET /connection leaked token: %s", get.Body.String())
	}
}

func TestGetGroupWithOrphanedMemberHealsAndSucceeds(t *testing.T) {
	ctx, memberID := setupCloudflareLogicDB(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.GET("/groups/:id", GetGroupHandler)

	member, err := repository.GetCFPointingMemberByID(ctx, memberID)
	if err != nil {
		t.Fatalf("GetCFPointingMemberByID() error = %v", err)
	}

	// Simulate orphaned member by deleting the ZoneDomain directly
	if err := repository.DB(ctx).Exec("DELETE FROM of_zone_domains WHERE id = ?", member.ZoneDomainID).Error; err != nil {
		t.Fatalf("DELETE FROM of_zone_domains error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/groups/1", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /groups/1 status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Verify the orphaned member has been removed
	_, err = repository.GetCFPointingMemberByID(ctx, memberID)
	if err == nil {
		t.Errorf("GetCFPointingMemberByID() should return not found after healing")
	}
}

func TestListGroupsHandlerWithMissingNodeStillSucceeds(t *testing.T) {
	ctx, _ := setupCloudflareLogicDB(t)
	if err := db.DB(ctx).Create(&model.CFPointingGroup{
		Name:          "KR",
		PrimaryNodeID: 12,
		ActiveNodeID:  12,
		Enabled:       true,
	}).Error; err != nil {
		t.Fatalf("Create(missing-node group) error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.GET("/groups", ListGroupsHandler)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/groups", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ListGroupsHandler status = %d, body = %s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"name":"KR"`) {
		t.Fatalf("ListGroupsHandler body = %s, want group KR", body)
	}
	if !strings.Contains(body, `"name":"primary"`) {
		t.Fatalf("ListGroupsHandler body = %s, want intact group primary", body)
	}
}

func TestGetGroupHandlerWithMissingNodeStillSucceeds(t *testing.T) {
	ctx, _ := setupCloudflareLogicDB(t)
	group := model.CFPointingGroup{Name: "KR", PrimaryNodeID: 12, ActiveNodeID: 12, Enabled: true}
	if err := db.DB(ctx).Create(&group).Error; err != nil {
		t.Fatalf("Create(missing-node group) error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.GET("/groups/:id", GetGroupHandler)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/groups/%d", group.ID), nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GetGroupHandler status = %d, body = %s, want %d", recorder.Code, recorder.Body.String(), http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `"name":"KR"`) {
		t.Fatalf("GetGroupHandler body = %s, want group KR", recorder.Body.String())
	}
}

func TestRoutersMoveMemberHandler(t *testing.T) {
	ctx, memberID := setupCloudflareLogicDB(t)

	restoreDispatch := SetDispatchTaskForTest(func(ctx context.Context, taskType string, payload []byte, triggeredBy string) (string, error) {
		return "mock-task-id", nil
	})
	t.Cleanup(restoreDispatch)

	fake := &fakeClient{}
	restoreClient := SetClientFactoryForTest(func(string) Client { return fake })
	t.Cleanup(restoreClient)

	member, err := repository.GetCFPointingMemberByID(ctx, memberID)
	if err != nil {
		t.Fatalf("GetCFPointingMemberByID() error = %v", err)
	}
	sourceGroupID := member.GroupID

	targetGroup := model.CFPointingGroup{
		Name:          "target-group",
		PrimaryNodeID: 1,
		ActiveNodeID:  1,
		Enabled:       true,
	}
	if err := db.DB(ctx).Create(&targetGroup).Error; err != nil {
		t.Fatalf("Create(targetGroup) error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.POST("/groups/:id/members/:memberId/move", MoveMemberHandler)

	t.Run("Success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/%d/move", sourceGroupID, memberID), strings.NewReader(fmt.Sprintf(`{"target_group_id":%d}`, targetGroup.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("MoveMemberHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"group_id":%d`, targetGroup.ID)) {
			t.Fatalf("MoveMemberHandler body = %s, want group_id %d", rec.Body.String(), targetGroup.ID)
		}

		updated, err := repository.GetCFPointingMemberByID(ctx, memberID)
		if err != nil {
			t.Fatalf("GetCFPointingMemberByID() error = %v", err)
		}
		if updated.GroupID != targetGroup.ID {
			t.Errorf("updated member GroupID = %d, want %d", updated.GroupID, targetGroup.ID)
		}
	})

	t.Run("InvalidTargetGroupSameAsSource", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/%d/move", targetGroup.ID, memberID), strings.NewReader(fmt.Sprintf(`{"target_group_id":%d}`, targetGroup.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("MoveMemberHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("InvalidTargetGroupNonExistent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/%d/move", targetGroup.ID, memberID), strings.NewReader(`{"target_group_id":99999}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("MoveMemberHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("InvalidParams", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/groups/abc/members/1/move", strings.NewReader(`{"target_group_id":1}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("MoveMemberHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("InvalidBody", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/%d/move", targetGroup.ID, memberID), strings.NewReader("invalid json"))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("MoveMemberHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})
}

func TestRoutersBatchMoveMembersHandler(t *testing.T) {
	ctx, member1ID := setupCloudflareLogicDB(t)

	restoreDispatch := SetDispatchTaskForTest(func(ctx context.Context, taskType string, payload []byte, triggeredBy string) (string, error) {
		return "mock-task-id", nil
	})
	t.Cleanup(restoreDispatch)

	fake := &fakeClient{}
	restoreClient := SetClientFactoryForTest(func(string) Client { return fake })
	t.Cleanup(restoreClient)

	member1, err := repository.GetCFPointingMemberByID(ctx, member1ID)
	if err != nil {
		t.Fatalf("GetCFPointingMemberByID() error = %v", err)
	}
	sourceGroupID := member1.GroupID

	targetGroup := model.CFPointingGroup{
		Name:          "batch-move-target",
		PrimaryNodeID: 1,
		ActiveNodeID:  1,
		Enabled:       true,
	}
	if err := db.DB(ctx).Create(&targetGroup).Error; err != nil {
		t.Fatalf("Create(targetGroup) error = %v", err)
	}

	domain2 := model.ZoneDomain{ZoneID: 1, Domain: "bm2.example.com"}
	if err := db.DB(ctx).Create(&domain2).Error; err != nil {
		t.Fatalf("Create(domain2) error = %v", err)
	}
	member2 := model.CFPointingMember{GroupID: sourceGroupID, ZoneDomainID: domain2.ID, Proxied: false, SyncStatus: model.CFMemberSyncOK}
	if err := db.DB(ctx).Create(&member2).Error; err != nil {
		t.Fatalf("Create(member2) error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.POST("/groups/:id/members/batch-move", BatchMoveMembersHandler)

	t.Run("Success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/batch-move", sourceGroupID), strings.NewReader(fmt.Sprintf(`{"member_ids":[%d,%d],"target_group_id":%d}`, member1.ID, member2.ID, targetGroup.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("BatchMoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
		}

		for _, mid := range []uint{member1.ID, member2.ID} {
			m, err := repository.GetCFPointingMemberByID(ctx, mid)
			if err != nil {
				t.Fatalf("GetCFPointingMemberByID(%d) error = %v", mid, err)
			}
			if m.GroupID != targetGroup.ID {
				t.Errorf("member %d GroupID = %d, want %d", mid, m.GroupID, targetGroup.ID)
			}
		}
	})

	t.Run("EmptyMemberIDs", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/batch-move", sourceGroupID), strings.NewReader(fmt.Sprintf(`{"member_ids":[],"target_group_id":%d}`, targetGroup.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("BatchMoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("TargetGroupSame", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/batch-move", targetGroup.ID), strings.NewReader(fmt.Sprintf(`{"member_ids":[%d],"target_group_id":%d}`, member1.ID, targetGroup.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("BatchMoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("InvalidParams", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/groups/xyz/members/batch-move", strings.NewReader(`{"member_ids":[1],"target_group_id":2}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("BatchMoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})
}

func TestRoutersBatchRemoveMembersHandler(t *testing.T) {
	ctx, member1ID := setupCloudflareLogicDB(t)

	fake := &fakeClient{}
	restoreClient := SetClientFactoryForTest(func(string) Client { return fake })
	t.Cleanup(restoreClient)

	member1, err := repository.GetCFPointingMemberByID(ctx, member1ID)
	if err != nil {
		t.Fatalf("GetCFPointingMemberByID() error = %v", err)
	}
	groupID := member1.GroupID

	domain2 := model.ZoneDomain{ZoneID: 1, Domain: "br2.example.com"}
	if err := db.DB(ctx).Create(&domain2).Error; err != nil {
		t.Fatalf("Create(domain2) error = %v", err)
	}
	member2 := model.CFPointingMember{GroupID: groupID, ZoneDomainID: domain2.ID, Proxied: false, SyncStatus: model.CFMemberSyncOK}
	if err := db.DB(ctx).Create(&member2).Error; err != nil {
		t.Fatalf("Create(member2) error = %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(response.ErrorHandlerMiddleware())
	router.POST("/groups/:id/members/batch-remove", BatchRemoveMembersHandler)

	t.Run("Success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/batch-remove", groupID), strings.NewReader(fmt.Sprintf(`{"member_ids":[%d,%d]}`, member1.ID, member2.ID)))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("BatchRemoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusOK)
		}

		for _, mid := range []uint{member1.ID, member2.ID} {
			_, err := repository.GetCFPointingMemberByID(ctx, mid)
			if err == nil {
				t.Errorf("member %d should have been deleted, but still found in DB", mid)
			}
		}
	})

	t.Run("EmptyMemberIDs", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/groups/%d/members/batch-remove", groupID), strings.NewReader(`{"member_ids":[]}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("BatchRemoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})

	t.Run("InvalidParams", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/groups/invalid/members/batch-remove", strings.NewReader(`{"member_ids":[1]}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("BatchRemoveMembersHandler status = %d, body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
		}
	})
}

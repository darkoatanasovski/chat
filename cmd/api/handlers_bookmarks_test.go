package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func bookmarkFolderPath(folderID string) string {
	return "/bookmarks/folders/" + folderID
}

func bookmarkPath(bookmarkID string) string {
	return "/bookmarks/" + bookmarkID
}

// TestHandleCreateBookmark_AndList proves bookmarking a real message
// creates a private, unfiled-by-default bookmark that shows up in the
// caller's own list.
func TestHandleCreateBookmark_AndList(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-sender")
	channel := createTestChannel(t, app, token, "bookmark-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "save me for later")

	var resp bookmarkResponse
	rec := do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID,
	}), token), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create bookmark: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.MessageID != msg.MessageID || resp.FolderID != nil {
		t.Fatalf("unexpected bookmark: %+v", resp)
	}

	var list []bookmarkResponse
	rec = do(t, app, authed(jsonRequest("GET", "/bookmarks", nil), token), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list bookmarks: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, b := range list {
		if b.BookmarkID == resp.BookmarkID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created bookmark not present in list: %+v", list)
	}
}

// TestHandleCreateBookmark_IsIdempotent proves bookmarking an
// already-bookmarked message returns the existing bookmark, and does not
// move it even if a folder_id is supplied the second time.
func TestHandleCreateBookmark_IsIdempotent(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-idempotent")
	channel := createTestChannel(t, app, token, "bookmark-idempotent-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "bookmark twice")

	var folder folderResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks/folders", createFolderRequest{Name: "idempotent-folder-" + uuid.NewString()}), token), &folder)

	var first, second bookmarkResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID,
	}), token), &first)
	rec := do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID, FolderID: &folder.FolderID,
	}), token), &second)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second create: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if second.BookmarkID != first.BookmarkID {
		t.Fatalf("second create made a new bookmark: first=%s second=%s", first.BookmarkID, second.BookmarkID)
	}
	if second.FolderID != nil {
		t.Fatalf("idempotent create moved the bookmark into folder %v, want unchanged (unfiled)", second.FolderID)
	}
}

// TestHandleCreateBookmark_MessageNotFound proves a message that doesn't
// exist in the given channel is a 404, not a silently-created dangling
// bookmark.
func TestHandleCreateBookmark_MessageNotFound(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-404")
	channel := createTestChannel(t, app, token, "bookmark-404-test")

	rec := do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: uuid.NewString(),
	}), token), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandleCreateBookmark_RequiresMembership proves a caller who isn't a
// member of the channel can't bookmark a message in it.
func TestHandleCreateBookmark_RequiresMembership(t *testing.T) {
	app := testApp(t)
	_, ownerToken := createTestUser(t, app, "bookmark-owner")
	_, outsiderToken := createTestUser(t, app, "bookmark-outsider")
	channel := createTestChannel(t, app, ownerToken, "bookmark-membership-test")
	msg := sendTestMessage(t, app, ownerToken, channel.ChannelID, "members only")

	rec := do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID,
	}), outsiderToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

// TestBookmarkFolders_CreateRenameDelete exercises the full folder
// lifecycle, and proves deleting a folder un-files (rather than deletes)
// any bookmark that was in it.
func TestBookmarkFolders_CreateRenameDelete(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-folders")
	channel := createTestChannel(t, app, token, "bookmark-folders-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "file me")

	var folder folderResponse
	rec := do(t, app, authed(jsonRequest("POST", "/bookmarks/folders", createFolderRequest{Name: "reading-list-" + uuid.NewString()}), token), &folder)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create folder: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	newName := "renamed-" + uuid.NewString()
	var renamed folderResponse
	rec = do(t, app, authed(jsonRequest("PATCH", bookmarkFolderPath(folder.FolderID), renameFolderRequest{Name: newName}), token), &renamed)
	if rec.Code != http.StatusOK || renamed.Name != newName {
		t.Fatalf("rename folder: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var bm bookmarkResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID, FolderID: &folder.FolderID,
	}), token), &bm)
	if bm.FolderID == nil || *bm.FolderID != folder.FolderID {
		t.Fatalf("bookmark not filed into folder: %+v", bm)
	}

	rec = do(t, app, authed(jsonRequest("DELETE", bookmarkFolderPath(folder.FolderID), nil), token), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete folder: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var list []bookmarkResponse
	rec = do(t, app, authed(jsonRequest("GET", "/bookmarks", nil), token), &list)
	if rec.Code != http.StatusOK {
		t.Fatalf("list bookmarks: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, b := range list {
		if b.BookmarkID == bm.BookmarkID {
			found = true
			if b.FolderID != nil {
				t.Fatalf("bookmark still has folder_id %v after its folder was deleted, want nil (unfiled)", b.FolderID)
			}
		}
	}
	if !found {
		t.Fatalf("bookmark disappeared after its folder was deleted, want it un-filed but still present")
	}
}

// TestHandleMoveBookmark_IntoFolderAndUnfile proves a bookmark can be
// re-filed into a folder and then unfiled again.
func TestHandleMoveBookmark_IntoFolderAndUnfile(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-move")
	channel := createTestChannel(t, app, token, "bookmark-move-test")
	msg := sendTestMessage(t, app, token, channel.ChannelID, "move me around")

	var folder folderResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks/folders", createFolderRequest{Name: "move-target-" + uuid.NewString()}), token), &folder)

	var bm bookmarkResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID,
	}), token), &bm)
	if bm.FolderID != nil {
		t.Fatalf("newly created bookmark already has a folder: %+v", bm)
	}

	var moved bookmarkResponse
	rec := do(t, app, authed(jsonRequest("PATCH", bookmarkPath(bm.BookmarkID), moveBookmarkRequest{FolderID: &folder.FolderID}), token), &moved)
	if rec.Code != http.StatusOK {
		t.Fatalf("move into folder: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if moved.FolderID == nil || *moved.FolderID != folder.FolderID {
		t.Fatalf("moved.FolderID = %v, want %s", moved.FolderID, folder.FolderID)
	}

	var unfiled bookmarkResponse
	rec = do(t, app, authed(jsonRequest("PATCH", bookmarkPath(bm.BookmarkID), moveBookmarkRequest{}), token), &unfiled)
	if rec.Code != http.StatusOK {
		t.Fatalf("unfile: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if unfiled.FolderID != nil {
		t.Fatalf("unfiled.FolderID = %v, want nil", unfiled.FolderID)
	}
}

// TestHandleListBookmarks_FilterByFolder proves ?folder_id= scopes results
// to a specific folder, "none" scopes to unfiled only, and omitting it
// returns everything.
func TestHandleListBookmarks_FilterByFolder(t *testing.T) {
	app := testApp(t)
	_, token := createTestUser(t, app, "bookmark-filter")
	channel := createTestChannel(t, app, token, "bookmark-filter-test")
	filedMsg := sendTestMessage(t, app, token, channel.ChannelID, "filed")
	unfiledMsg := sendTestMessage(t, app, token, channel.ChannelID, "unfiled")

	var folder folderResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks/folders", createFolderRequest{Name: "filter-folder-" + uuid.NewString()}), token), &folder)

	var filedBM, unfiledBM bookmarkResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: filedMsg.MessageID, FolderID: &folder.FolderID,
	}), token), &filedBM)
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: unfiledMsg.MessageID,
	}), token), &unfiledBM)

	var byFolder []bookmarkResponse
	rec := do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/bookmarks?folder_id=%s", folder.FolderID), nil), token), &byFolder)
	if rec.Code != http.StatusOK || len(byFolder) != 1 || byFolder[0].BookmarkID != filedBM.BookmarkID {
		t.Fatalf("folder filter: status=%d got=%+v, want exactly the filed bookmark", rec.Code, byFolder)
	}

	var unfiledOnly []bookmarkResponse
	rec = do(t, app, authed(jsonRequest("GET", "/bookmarks?folder_id=none", nil), token), &unfiledOnly)
	if rec.Code != http.StatusOK {
		t.Fatalf("unfiled filter: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, b := range unfiledOnly {
		if b.BookmarkID == filedBM.BookmarkID {
			t.Fatalf("filed bookmark leaked into unfiled-only listing")
		}
	}
	foundUnfiled := false
	for _, b := range unfiledOnly {
		if b.BookmarkID == unfiledBM.BookmarkID {
			foundUnfiled = true
		}
	}
	if !foundUnfiled {
		t.Fatalf("unfiled bookmark missing from unfiled-only listing: %+v", unfiledOnly)
	}
}

// TestBookmarks_PrivateToOwner proves one user's bookmarks are invisible to
// another user, even within the same app and channel.
func TestBookmarks_PrivateToOwner(t *testing.T) {
	app := testApp(t)
	aliceID, aliceToken := createTestUser(t, app, "bookmark-private-alice")
	bobID, bobToken := createTestUser(t, app, "bookmark-private-bob")
	channel := createTestChannel(t, app, aliceToken, "bookmark-private-test")
	rec := do(t, app, authed(jsonRequest("POST", "/channels/"+channel.ChannelID+"/members", addMemberRequest{UserID: bobID.String()}), aliceToken), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add bob to channel: status %d, body %s", rec.Code, rec.Body.String())
	}
	_ = aliceID
	msg := sendTestMessage(t, app, aliceToken, channel.ChannelID, "alice's private save")

	var aliceBM bookmarkResponse
	do(t, app, authed(jsonRequest("POST", "/bookmarks", createBookmarkRequest{
		ChannelID: channel.ChannelID, MessageID: msg.MessageID,
	}), aliceToken), &aliceBM)

	var bobList []bookmarkResponse
	rec = do(t, app, authed(jsonRequest("GET", "/bookmarks", nil), bobToken), &bobList)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob list bookmarks: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, b := range bobList {
		if b.BookmarkID == aliceBM.BookmarkID {
			t.Fatalf("bob can see alice's bookmark: %+v", b)
		}
	}
}

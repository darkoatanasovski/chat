// Dashboard-driven polls oversight: a read-only view of poll activity
// within one app's channels — same bounded scatter-gather-over-physical-
// shards shape as handleDashboardAppMessages, since poll storage lives on
// the shard databases (internal/polls), not the control plane. There's no
// dashboard-driven poll *creation*: polls are created by an app's own
// end-users (POST /channels/{id}/polls), same division of labor as
// messages themselves.
package api

import (
	"net/http"
	"sort"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/polls"
)

// dashboardPollsLimit caps how many of an app's most recent polls this
// view returns, after merging every physical shard's own top results —
// an admin overview, not a paginated feed.
const dashboardPollsLimit = 50

type dashboardPollResponse struct {
	PollID      string               `json:"poll_id"`
	ChannelID   string               `json:"channel_id"`
	ChannelName string               `json:"channel_name"`
	AppID       int64                `json:"app_id"`
	AppName     string               `json:"app_name"`
	Question    string               `json:"question"`
	MultiSelect bool                 `json:"multi_select"`
	ClosesAt    *string              `json:"closes_at,omitempty"`
	CreatedAt   string               `json:"created_at"`
	Options     []pollOptionResponse `json:"options"`
	TotalVoters int                  `json:"total_voters"`
}

// dashboardPollsFor computes the most recent polls (up to
// dashboardPollsLimit) across every channel belonging to appIDs — the
// shared core of the per-app dashboard tab's polls panel (a single-element
// appIDs) and, previously, an org-wide aggregate across every app (now
// retired in favor of the per-app view — see handleDashboardAppPolls).
// appNameByID only needs entries for the ids in appIDs; it's threaded
// through separately (rather than re-derived here) so a single-app caller
// can build a one-entry map instead of loading its whole org's app list.
func (a *App) dashboardPollsFor(r *http.Request, appIDs []int64, appNameByID map[int64]string) ([]dashboardPollResponse, error) {
	routeInfo, err := a.channelsRepo.ListRouteInfoByApps(r.Context(), appIDs)
	if err != nil {
		return nil, err
	}

	var channelIDs []uuid.UUID
	appByChannel := map[uuid.UUID]int64{}
	nameByChannel := map[uuid.UUID]string{}
	for _, c := range routeInfo {
		channelIDs = append(channelIDs, c.ChannelID)
		appByChannel[c.ChannelID] = c.AppID
		nameByChannel[c.ChannelID] = c.Name
	}

	// All these channels live in this cell's database.
	var all []polls.Poll
	if len(channelIDs) > 0 {
		list, err := a.pollsRepo.ListByChannels(r.Context(), a.cellPool, channelIDs, dashboardPollsLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, list...)
	}

	// ListByChannels already applies the cap; sort newest-first before
	// truncating to the same cap for a stable overall ordering.
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if len(all) > dashboardPollsLimit {
		all = all[:dashboardPollsLimit]
	}

	out := make([]dashboardPollResponse, len(all))
	for i, p := range all {
		var closesAt *string
		if p.ClosesAt != nil {
			s := p.ClosesAt.Format(rfc3339Milli)
			closesAt = &s
		}
		appID := appByChannel[p.ChannelID]
		out[i] = dashboardPollResponse{
			PollID:      p.PollID.String(),
			ChannelID:   p.ChannelID.String(),
			ChannelName: nameByChannel[p.ChannelID],
			AppID:       appID,
			AppName:     appNameByID[appID],
			Question:    p.Question,
			MultiSelect: p.MultiSelect,
			ClosesAt:    closesAt,
			CreatedAt:   p.CreatedAt.Format(rfc3339Milli),
			Options:     toPollOptionResponse(p.Options),
			TotalVoters: p.TotalVoters,
		}
	}
	return out, nil
}

// handleDashboardAppPolls backs GET /dashboard/apps/{app_id}/polls — one
// app's own recent polls, for its Dashboard tab. Scoped to a single app
// rather than the org's whole app list, same "per app, not per org" shape
// as handleDashboardListChannels/handleDashboardListBlocks.
func (a *App) handleDashboardAppPolls(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	out, err := a.dashboardPollsFor(r, []int64{app.AppID}, map[int64]string{app.AppID: app.Name})
	if err != nil {
		a.log.Error("load app polls", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load polls")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

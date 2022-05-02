package main

import (
	"errors"
	_ "github.com/joho/godotenv/autoload"
	"os"
	"strings"
)

type user struct {
	lists   []dataPair
	ratings dataPair
}

type dataPair struct {
	imdbList     []imdbItem
	imdbListId   string
	imdbListName string
	traktList    []traktItem
	traktListId  string
	isWatchlist  bool
}

func sync() {
	ic := newImdbClient()
	tc := newTraktClient()
	u := &user{}
	u.populateData(ic, tc)
	u.syncLists(tc)
	u.syncRatings(tc)
}

func (u *user) populateData(ic *imdbClient, tc *traktClient) {
	ic.config.imdbUserId = ic.userIdScrape()
	ic.config.imdbWatchlistId = ic.watchlistIdScrape()
	tc.config.traktUserId = tc.userIdGet()
	imdbListIdsString := os.Getenv(imdbListIdsKey)
	switch imdbListIdsString {
	case "all":
		u.lists = ic.listsScrape()
	default:
		imdbListIds := strings.Split(imdbListIdsString, ",")
		u.lists = cleanupLists(ic, imdbListIds)
	}
	_, imdbList, _ := ic.listItemsGet(ic.config.imdbWatchlistId)
	u.lists = append(u.lists, dataPair{
		imdbList:     imdbList,
		imdbListId:   ic.config.imdbWatchlistId,
		imdbListName: "watchlist",
		isWatchlist:  true,
	})
	for i := range u.lists {
		list := &u.lists[i]
		if list.isWatchlist {
			list.traktList = tc.watchlistItemsGet()
			continue
		}
		traktList, err := tc.listItemsGet(list.traktListId)
		if errors.Is(err, errNotFound) {
			tc.listAdd(list.traktListId, list.imdbListName)
		}
		list.traktList = traktList
	}
	u.ratings = dataPair{
		imdbList:  ic.ratingsGet(),
		traktList: tc.ratingsGet(),
	}
}

func (u *user) syncLists(tc *traktClient) {
	for _, list := range u.lists {
		diff := list.difference()
		if len(diff["add"]) > 0 {
			if list.isWatchlist {
				tc.watchlistItemsAdd(diff["add"])
				continue
			}
			tc.listItemsAdd(list.traktListId, diff["add"])
		}
		if len(diff["remove"]) > 0 {
			if list.isWatchlist {
				tc.watchlistItemsRemove(diff["remove"])
				continue
			}
			tc.listItemsRemove(list.traktListId, diff["remove"])
		}
	}
	// Remove lists that only exist in Trakt
	traktLists := tc.listsGet()
	for _, tl := range traktLists {
		if !contains(u.lists, tl.Name) {
			tc.listRemove(tl.Ids.Slug)
		}
	}
}

func (u *user) syncRatings(tc *traktClient) {
	diff := u.ratings.difference()
	if len(diff["add"]) > 0 {
		tc.ratingsAdd(diff["add"])
		for _, ti := range diff["add"] {
			history := tc.historyGet(ti)
			if len(history) > 0 {
				continue
			}
			tc.historyAdd([]traktItem{ti})
		}
	}
	if len(diff["remove"]) > 0 {
		tc.ratingsRemove(diff["remove"])
		for _, ti := range diff["remove"] {
			history := tc.historyGet(ti)
			if len(history) == 0 {
				continue
			}
			tc.historyRemove([]traktItem{ti})
		}
	}
}

// cleanupLists removes invalid imdb lists passed via the IMDB_LIST_IDS
// env variable and returns only the lists that actually exist
func cleanupLists(ic *imdbClient, imdbListIds []string) []dataPair {
	lists := make([]dataPair, len(imdbListIds))
	n := 0
	for _, imdbListId := range imdbListIds {
		imdbListName, imdbList, err := ic.listItemsGet(imdbListId)
		if errors.Is(err, errNotFound) {
			continue
		}
		lists[n] = dataPair{
			imdbList:     imdbList,
			imdbListId:   imdbListId,
			imdbListName: *imdbListName,
			traktListId:  formatTraktListName(*imdbListName),
		}
		n++
	}
	return lists[:n]
}

func (dp *dataPair) difference() map[string][]traktItem {
	diff := make(map[string][]traktItem)
	// add missing items to trakt
	temp := make(map[string]struct{})
	for _, tlItem := range dp.traktList {
		switch tlItem.Type {
		case "movie":
			temp[tlItem.Movie.Ids.Imdb] = struct{}{}
		case "show":
			temp[tlItem.Show.Ids.Imdb] = struct{}{}
		case "episode":
			temp[tlItem.Episode.Ids.Imdb] = struct{}{}
		default:
			continue
		}
	}
	for _, ilItem := range dp.imdbList {
		if _, found := temp[ilItem.id]; !found {
			ti := traktItem{}
			tiSpec := traktItemSpec{
				Ids: Ids{
					Imdb: ilItem.id,
				},
			}
			if ilItem.rating != nil {
				ti.WatchedAt = ilItem.ratingDate.UTC().String()
				tiSpec.RatedAt = ilItem.ratingDate.UTC().String()
				tiSpec.Rating = *ilItem.rating
			}
			switch ilItem.titleType {
			case "movie":
				ti.Type = "movie"
				ti.Movie = tiSpec
			case "tvSeries":
				ti.Type = "show"
				ti.Show = tiSpec
			case "tvMiniSeries":
				ti.Type = "show"
				ti.Show = tiSpec
			case "tvEpisode":
				ti.Type = "episode"
				ti.Episode = tiSpec
			default:
				ti.Type = "movie"
				ti.Movie = tiSpec
			}
			diff["add"] = append(diff["add"], ti)
		}
	}
	// remove out of sync items from trakt
	temp = make(map[string]struct{})
	for _, ilItem := range dp.imdbList {
		temp[ilItem.id] = struct{}{}
	}
	for _, tlItem := range dp.traktList {
		var itemId string
		switch tlItem.Type {
		case "movie":
			itemId = tlItem.Movie.Ids.Imdb
		case "show":
			itemId = tlItem.Show.Ids.Imdb
		case "episode":
			itemId = tlItem.Episode.Ids.Imdb
		default:
			continue
		}
		if _, found := temp[itemId]; !found {
			diff["remove"] = append(diff["remove"], tlItem)
		}
	}
	return diff
}

func contains(dps []dataPair, traktListName string) bool {
	for _, dp := range dps {
		if dp.imdbListName == traktListName {
			return true
		}
	}
	return false
}

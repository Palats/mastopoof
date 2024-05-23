package testserver

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func parseID(statusID string) (int64, error) {
	id, err := strconv.ParseInt(string(statusID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse mastodon ID %q: %w", statusID, err)
	}
	return id, nil
}

type Entity[T any] struct {
	id    string
	key   int64
	Value T
}

func cmpEntities[T any](e1 *Entity[T], e2 *Entity[T]) int {
	if e1.key < e2.key {
		return -1
	}
	if e1.key > e2.key {
		return 1
	}
	return 0
}

// EntityList manages a sorted list, suitable for being queried with pagination.
type EntityList[T any] struct {
	entities []*Entity[T]
}

func (list *EntityList[T]) idx(id string) (int, error) {
	if id == "" {
		return -1, nil
	}
	key, err := parseID(id)
	if err != nil {
		return -1, fmt.Errorf("%q is not a valid mastodon ID: %w", id, err)
	}

	entity := &Entity[T]{key: key}
	idx, found := slices.BinarySearchFunc(list.entities, entity, cmpEntities)
	if !found {
		return -1, nil
	}
	return idx, nil
}

// CreateNextID gives back an ID suitable for this list which would insert
// an entity at the end of the list. The ID is not reserved, so multiple call
// to this function without insertion might return the same value.
func (list *EntityList[T]) CreateNextID() string {
	if len(list.entities) == 0 {
		return "1"
	}
	return strconv.FormatInt(list.entities[len(list.entities)-1].key+1, 10)
}

// Insert a new value in the list, keeping it sorted.
func (list *EntityList[T]) Insert(value T, id string) error {
	key, err := parseID(id)
	if err != nil {
		return err
	}
	entity := &Entity[T]{
		id:    id,
		key:   key,
		Value: value,
	}

	idx, found := slices.BinarySearchFunc(list.entities, entity, cmpEntities)
	if found {
		return fmt.Errorf("duplicate ID %d", entity.key)
	}
	list.entities = slices.Insert(list.entities, idx, entity)
	return nil
}

// List manages a listing requests as a mastodon server.
// It takes care of pagination, and returns:
//   - The list of matching values
//   - The content for the `Link` header (can be empty)
//   - or an error
func (list *EntityList[T]) List(req *http.Request, linkPath string) ([]T, string, error) {
	maxIDidx, err := list.idx(req.URL.Query().Get("max_id"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid max_id parameter: %v", err)
	}

	sinceIDidx, err := list.idx(req.URL.Query().Get("since_id"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid since_id parameter: %v", err)
	}

	minIDidx, err := list.idx(req.URL.Query().Get("min_id"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid min_id parameter: %v", err)
	}

	limit := 20
	sLimit := req.URL.Query().Get("limit")
	if sLimit != "" {
		l64, err := strconv.ParseInt(sLimit, 10, strconv.IntSize)
		if err != nil {
			return nil, "", fmt.Errorf("invalid limit parameter: %v", err)
		}
		limit = int(l64)
	}

	firstIdx := 0                 // Included
	lastIdx := len(list.entities) // Not included
	if minIDidx >= 0 {
		firstIdx = minIDidx
		if sinceIDidx >= 0 && sinceIDidx > firstIdx {
			firstIdx = sinceIDidx
		}
		// min_id and since_id are not included when set.
		firstIdx++

		lastIdx = firstIdx + limit
		if maxIDidx >= 0 && maxIDidx <= lastIdx {
			lastIdx = maxIDidx
		}
		if lastIdx > len(list.entities) {
			lastIdx = len(list.entities)
		}
	} else {
		// min_idx is not set, so we go backward from recent statuses.
		lastIdx = len(list.entities)
		if maxIDidx >= 0 && maxIDidx <= lastIdx {
			lastIdx = maxIDidx
		}

		firstIdx = lastIdx - limit
		if sinceIDidx >= 0 && sinceIDidx >= firstIdx {
			firstIdx = sinceIDidx + 1
		}
		if firstIdx < 0 {
			firstIdx = 0
		}
	}

	values := []T{}
	// Return entities from highest ID to lowest - i.e., in reverse chronological order,
	// which is likely similar to Mastodon.
	for i := lastIdx - 1; i >= firstIdx; i-- {
		values = append(values, list.entities[i].Value)
	}

	// See https://docs.joinmastodon.org/api/guidelines/#pagination for how `Link` header
	// is used by Mastodon. 2 links can be provided:
	//  - `next` should get older results. Older results means lower ID. It seems that only `max_id` is expected on this URL.
	//  - `prev` should get new results. This means higher ID. It seems that `since_id` and `min_id` are used on this URL.
	// The idea seems to be that Mastodon returns results in reverse
	// chronological order - i.e., from most recent to oldest. In turn, it means
	// that next gives older results.
	var linkEntries []string

	// 'next' returns older results - thus results with smaller Status ID, which means lower
	// index in the s.items array. The result is clamped by `max_id`. The `max_id` is excluded
	// from the result, so that can be directly the oldest status returned here.
	if firstIdx >= 0 && firstIdx < len(list.entities) && firstIdx < lastIdx {
		var uNext url.URL
		uNext.Scheme = "https"
		uNext.Host = "localhost"
		uNext.Path = linkPath
		q := uNext.Query()
		q.Set("max_id", list.entities[firstIdx].id)
		uNext.RawQuery = q.Encode()
		linkEntries = append(linkEntries, fmt.Sprintf("<%s>; rel=\"next\"", uNext.String()))
	}

	// 'prev' returns newer results - thus results with higher status ID, which means
	// higher index in the sorted s.items array.
	// `min_id` is excluded from the results, so it can be directly the most recent
	// status which is returned there. Same for `since_id`.
	// Note that `lastIdx` is excluded from result.
	if lastIdx-1 >= 0 && lastIdx-1 < len(list.entities) && firstIdx < lastIdx {
		var uPrev url.URL
		uPrev.Scheme = "https"
		uPrev.Host = "localhost"
		uPrev.Path = linkPath

		id := list.entities[lastIdx-1].id
		q := uPrev.Query()
		q.Set("min_id", id)
		// Don't set `since_id`, like official Mastodon server.
		uPrev.RawQuery = q.Encode()
		linkEntries = append(linkEntries, fmt.Sprintf("<%s>; rel=\"prev\"", uPrev.String()))
	}

	return values, strings.Join(linkEntries, ", "), nil
}

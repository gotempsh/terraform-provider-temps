// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	// listPageSize is the largest page_size Temps serves; it clamps larger
	// requests to it.
	listPageSize = 100
	// maxListPages bounds a list read, so a server that keeps answering
	// with full pages cannot keep the provider reading forever.
	maxListPages = 1000
	// listWalks is how many times a list is read when it changes during
	// the read and the item was not found.
	listWalks = 3
)

// listPage is one answer from a list Temps pages from 2026-11-25 on, as
// {"items": [...], "total": n, "page": p, "page_size": s}, and earlier
// releases sent whole as a bare array.
type listPage[T any] struct {
	Items []T
	// Paged is false for a bare array, which is the whole list.
	Paged bool
	// Total is the number of items across all pages, or -1 when the
	// envelope does not say.
	Total int64
	// Page and PageSize are what the server served; 0 when it does not say.
	Page     int64
	PageSize int64
}

// UnmarshalJSON accepts a bare array or a page envelope with an items
// array, and rejects anything else, including null.
func (p *listPage[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var items []T
		if err := json.Unmarshal(b, &items); err != nil {
			return err
		}
		*p = listPage[T]{Items: items, Total: -1}
		return nil
	}
	if len(b) > 0 && b[0] == '{' {
		var env struct {
			Items    *[]T   `json:"items"`
			Total    *int64 `json:"total"`
			Page     int64  `json:"page"`
			PageSize int64  `json:"page_size"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return err
		}
		if env.Items == nil {
			return errors.New("list response is an object without an items array")
		}
		*p = listPage[T]{Items: *env.Items, Paged: true, Total: -1, Page: env.Page, PageSize: env.PageSize}
		if env.Total != nil {
			p.Total = *env.Total
		}
		return nil
	}
	return errors.New("list response is neither an array nor a page of items")
}

// findInList reads the list at path page by page, at the largest page size,
// and returns the first item match accepts, or nil when no page has one.
//
// Temps lists newest first, so an item deleted while the list is read moves
// every older one a place forward, possibly onto a page already read. When
// the list's total changes during a read that found nothing, the list is
// read again.
func findInList[T any](ctx context.Context, c *Client, path string, query url.Values, match func(*T) bool) (*T, error) {
	for range listWalks {
		found, stable, err := walkList(ctx, c, path, query, match)
		if err != nil || found != nil || stable {
			return found, err
		}
	}
	return nil, fmt.Errorf("GET %s: the list changed each of the %d times it was read; try again", path, listWalks)
}

// walkList is one read of the list for findInList. stable reports whether
// the list's total stayed the same across the pages read.
func walkList[T any](ctx context.Context, c *Client, path string, query url.Values, match func(*T) bool) (found *T, stable bool, err error) {
	stable = true
	total := int64(-1)
	for page := int64(1); page <= maxListPages; page++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", strconv.FormatInt(page, 10))
		q.Set("page_size", strconv.Itoa(listPageSize))
		var got listPage[T]
		if err := c.do(ctx, http.MethodGet, path, q, nil, &got); err != nil {
			return nil, false, err
		}
		for i := range got.Items {
			if match(&got.Items[i]) {
				return &got.Items[i], true, nil
			}
		}
		if !got.Paged {
			return nil, true, nil // a bare array is the whole list
		}
		if got.Page != 0 && got.Page != page {
			return nil, false, fmt.Errorf("GET %s: asked for page %d, the server answered with page %d", path, page, got.Page)
		}
		if got.Total >= 0 {
			if total >= 0 && got.Total != total {
				stable = false
			}
			total = got.Total
		}
		size := got.PageSize
		if size <= 0 {
			size = listPageSize
		}
		if len(got.Items) == 0 || int64(len(got.Items)) < size || (total >= 0 && page*size >= total) {
			return nil, stable, nil
		}
	}
	return nil, false, fmt.Errorf("GET %s: more items remain after %d pages of %d; stopped reading", path, maxListPages, listPageSize)
}

// Copyright (c) 2012-present The upper.io/db authors. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining
// a copy of this software and associated documentation files (the
// "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to
// the following conditions:
//
// The above copyright notice and this permission notice shall be
// included in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
// EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
// OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
// WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package mongo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"encoding/json"

	db "github.com/upper/db/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/upper/db/v4/internal/immutable"
)

type resultQuery struct {
	c *Collection

	fields     []string
	limit      int
	offset     int
	sort       []string
	conditions interface{}
	groupBy    []interface{}

	pageSize           uint
	pageNumber         uint
	cursorColumn       string
	cursorValue        interface{}
	cursorCond         db.Cond
	cursorReverseOrder bool
}

type result struct {
	cur *mongo.Cursor

	err   error
	errMu sync.Mutex

	fn   func(*resultQuery) error
	prev *result
}

var _ = immutable.Immutable(&result{})

func (res *result) frame(fn func(*resultQuery) error) *result {
	return &result{prev: res, fn: fn}
}

func (r *resultQuery) and(terms ...interface{}) error {
	if r.conditions == nil {
		return r.where(terms...)
	}

	r.conditions = map[string]interface{}{
		"$and": []interface{}{
			r.conditions,
			r.c.compileQuery(terms...),
		},
	}
	return nil
}

func (r *resultQuery) where(terms ...interface{}) error {
	r.conditions = r.c.compileQuery(terms...)
	return nil
}

func (res *result) And(terms ...interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		return r.and(terms...)
	})
}

func (res *result) Where(terms ...interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		return r.where(terms...)
	})
}

func (res *result) Paginate(pageSize uint) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.pageSize = pageSize
		return nil
	})
}

func (res *result) Page(pageNumber uint) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.pageNumber = pageNumber
		return nil
	})
}

func (res *result) Cursor(cursorColumn string) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.cursorColumn = cursorColumn
		return nil
	})
}

func (res *result) NextPage(cursorValue interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.cursorValue = cursorValue
		r.cursorReverseOrder = false
		r.cursorCond = db.Cond{
			r.cursorColumn: bson.M{"$gt": cursorValue},
		}
		return nil
	})
}

func (res *result) PrevPage(cursorValue interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.cursorValue = cursorValue
		r.cursorReverseOrder = true
		r.cursorCond = db.Cond{
			r.cursorColumn: bson.M{"$lt": cursorValue},
		}
		return nil
	})
}

func (res *result) TotalEntries() (uint64, error) {
	return res.Count()
}

func (res *result) TotalPages() (uint, error) {
	count, err := res.Count()
	if err != nil {
		return 0, err
	}

	rq, err := res.build()
	if err != nil {
		return 0, err
	}

	if rq.pageSize < 1 {
		return 1, nil
	}

	total := uint(math.Ceil(float64(count) / float64(rq.pageSize)))
	return total, nil
}

// Limit determines the maximum limit of results to be returned.
func (res *result) Limit(n int) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.limit = n
		return nil
	})
}

// Offset determines how many documents will be skipped before starting to grab
// results.
func (res *result) Offset(n int) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.offset = n
		return nil
	})
}

// OrderBy determines sorting of results according to the provided names. Fields
// may be prefixed by - (minus) which means descending order, ascending order
// would be used otherwise.
func (res *result) OrderBy(fields ...interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		ss := make([]string, len(fields))
		for i, field := range fields {
			ss[i] = fmt.Sprintf(`%v`, field)
		}
		r.sort = ss
		return nil
	})
}

// String satisfies fmt.Stringer
func (res *result) String() string {
	return ""
}

// Select marks the specific fields the user wants to retrieve.
func (res *result) Select(fields ...interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		fieldslen := len(fields)
		r.fields = make([]string, 0, fieldslen)
		for i := 0; i < fieldslen; i++ {
			r.fields = append(r.fields, fmt.Sprintf(`%v`, fields[i]))
		}
		return nil
	})
}

// All dumps all results into a pointer to an slice of structs or maps.
func (res *result) All(dst interface{}) error {
	rq, err := res.build()
	if err != nil {
		return err
	}

	q, err := rq.query()
	if err != nil {
		return err
	}

	defer func(start time.Time) {
		queryLog(&db.QueryStatus{
			RawQuery: rq.debugQuery("Find.All"),
			Err:      err,
			Start:    start,
			End:      time.Now(),
		})
	}(time.Now())

	err = q.All(context.Background(), dst)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return db.ErrNoMoreRows
	}

	return err
}

// GroupBy is used to group results that have the same value in the same column
// or columns.
func (res *result) GroupBy(fields ...interface{}) db.Result {
	return res.frame(func(r *resultQuery) error {
		r.groupBy = fields
		return nil
	})
}

// One fetches only one result from the resultset.
func (res *result) One(dst interface{}) error {
	ctx := context.Background()

	rq, err := res.build()
	if err != nil {
		return err
	}

	q, err := rq.query()
	if err != nil {
		return err
	}

	defer func(start time.Time) {
		queryLog(&db.QueryStatus{
			RawQuery: rq.debugQuery("Find.One"),
			Err:      err,
			Start:    start,
			End:      time.Now(),
		})
	}(time.Now())

	if !q.Next(ctx) {
		if q.Err() != nil {
			return q.Err()
		}
		return db.ErrNoMoreRows
	}

	defer q.Close(ctx)

	err = q.Decode(dst)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return db.ErrNoMoreRows
	}

	return err
}

func (res *result) Err() error {
	res.errMu.Lock()
	defer res.errMu.Unlock()

	return res.err
}

func (res *result) setErr(err error) {
	res.errMu.Lock()
	defer res.errMu.Unlock()
	res.err = err
}

func (res *result) Next(dst interface{}) bool {
	ctx := context.Background()

	if res.cur == nil {
		rq, err := res.build()
		if err != nil {
			return false
		}

		q, err := rq.query()
		if err != nil {
			return false
		}

		defer func(start time.Time) {
			queryLog(&db.QueryStatus{
				RawQuery: rq.debugQuery("Find.Next"),
				Err:      err,
				Start:    start,
				End:      time.Now(),
			})
		}(time.Now())

		res.cur = q
	}

	if !res.cur.Next(ctx) {
		res.setErr(res.cur.Err())
		return false
	}

	err := res.cur.Decode(dst)
	if err != nil {
		res.setErr(err)
		return false
	}

	return true
}

// Delete remove the matching items from the collection.
func (res *result) Delete() error {
	ctx := context.Background()

	rq, err := res.build()
	if err != nil {
		return err
	}

	defer func(start time.Time) {
		queryLog(&db.QueryStatus{
			RawQuery: rq.debugQuery("Remove"),
			Err:      err,
			Start:    start,
			End:      time.Now(),
		})
	}(time.Now())

	_, err = rq.c.collection.DeleteMany(ctx, rq.conditions)
	if err != nil {
		return err
	}

	return nil
}

// Close closes the result set.
func (r *result) Close() error {
	ctx := context.Background()
	var err error

	if r.cur != nil {
		err = r.cur.Close(ctx)
		r.cur = nil
	}

	return err
}

// Update modified matching items from the collection with values of the given
// map or struct.
func (res *result) Update(src interface{}) (err error) {
	ctx := context.Background()

	updateSet := map[string]interface{}{"$set": src}

	rq, err := res.build()
	if err != nil {
		return err
	}

	defer func(start time.Time) {
		queryLog(&db.QueryStatus{
			RawQuery: rq.debugQuery("Update"),
			Err:      err,
			Start:    start,
			End:      time.Now(),
		})
	}(time.Now())

	_, err = rq.c.collection.UpdateMany(ctx, rq.conditions, updateSet)
	if err != nil {
		return err
	}
	return nil
}

func (res *result) build() (*resultQuery, error) {
	rqi, err := immutable.FastForward(res)
	if err != nil {
		return nil, err
	}

	rq := rqi.(*resultQuery)
	if !rq.cursorCond.Empty() {
		if err := rq.and(rq.cursorCond); err != nil {
			return nil, err
		}
	}

	if rq.cursorColumn != "" {
		if rq.cursorReverseOrder {
			rq.sort = append(rq.sort, "-"+rq.cursorColumn)
		} else {
			rq.sort = append(rq.sort, rq.cursorColumn)
		}
	}

	if rq.conditions == nil {
		rq.conditions = bson.D{}
	}

	return rq, nil
}

// query executes a mongo query.
func (r *resultQuery) query() (*mongo.Cursor, error) {
	ctx := context.Background()

	opts := options.Find()

	if len(r.groupBy) > 0 {
		return nil, db.ErrUnsupported
	}

	if r.pageSize > 0 {
		r.offset = int(r.pageSize * r.pageNumber)
		r.limit = int(r.pageSize)
	}

	if r.offset > 0 {
		opts.SetSkip(int64(r.offset))
	}

	if r.limit > 0 {
		opts.SetLimit(int64(r.limit))
	}

	if len(r.sort) > 0 {
		sort := bson.D{}
		for _, field := range r.sort {
			key, value := field, 1
			if key[0] == '-' {
				key, value = key[1:], -1
			}
			sort = append(sort, bson.E{Key: key, Value: value})
		}
		opts.SetSort(sort)
	}

	selectedFields := bson.M{}
	if len(r.fields) > 0 {
		for _, field := range r.fields {
			if field == `*` {
				break
			}
			selectedFields[field] = true
		}
	}

	if r.cursorReverseOrder {
		opts.SetSort(bson.D{{Key: "_id", Value: -1}})
	}

	if len(selectedFields) > 0 {
		panic("not implemented")
	}

	q, err := r.c.collection.Find(ctx, r.conditions, opts)
	if err != nil {
		return nil, err
	}

	return q, nil
}

func (r *resultQuery) count() (int64, error) {
	ctx := context.Background()

	opts := options.Count()

	if len(r.groupBy) > 0 {
		return 0, db.ErrUnsupported
	}

	n, err := r.c.collection.CountDocuments(ctx, r.conditions, opts)
	if err != nil {
		return 0, fmt.Errorf("CountDocuments: %w", err)
	}

	return n, nil
}

func (res *result) Exists() (bool, error) {
	total, err := res.Count()
	if err != nil {
		return false, err
	}

	if total > 0 {
		return true, nil
	}

	return false, nil
}

// Count counts matching elements.
func (res *result) Count() (total uint64, err error) {
	if res == nil {
		return 0, nil
	}

	rq, err := res.build()
	if err != nil {
		return 0, err
	}

	defer func(start time.Time) {
		queryLog(&db.QueryStatus{
			RawQuery: rq.debugQuery("Count"),
			Err:      err,
			Start:    start,
			End:      time.Now(),
		})
	}(time.Now())

	count, err := rq.count()
	if err != nil {
		return 0, err
	}

	return uint64(count), nil
}

func (res *result) Prev() immutable.Immutable {
	if res == nil {
		return nil
	}
	return res.prev
}

func (res *result) Fn(in interface{}) error {
	if res.fn == nil {
		return nil
	}
	return res.fn(in.(*resultQuery))
}

func (res *result) Base() interface{} {
	return &resultQuery{}
}

func (r *resultQuery) debugQuery(action string) string {
	query := fmt.Sprintf("db.%s.%s", r.c.collection.Name(), action)

	if r.conditions != nil {
		query = fmt.Sprintf("%s.conds(%v)", query, r.conditions)
	}
	if r.limit > 0 {
		query = fmt.Sprintf("%s.limit(%d)", query, r.limit)
	}
	if r.offset > 0 {
		query = fmt.Sprintf("%s.offset(%d)", query, r.offset)
	}
	if len(r.fields) > 0 {
		selectedFields := bson.M{}
		for _, field := range r.fields {
			if field == `*` {
				break
			}
			selectedFields[field] = true
		}
		if len(selectedFields) > 0 {
			query = fmt.Sprintf("%s.select(%v)", query, selectedFields)
		}
	}
	if len(r.groupBy) > 0 {
		escaped := make([]string, len(r.groupBy))
		for i := range r.groupBy {
			escaped[i] = string(mustJSON(r.groupBy[i]))
		}
		query = fmt.Sprintf("%s.groupBy(%v)", query, strings.Join(escaped, ", "))
	}
	if len(r.sort) > 0 {
		escaped := make([]string, len(r.sort))
		for i := range r.sort {
			escaped[i] = string(mustJSON(r.sort[i]))
		}
		query = fmt.Sprintf("%s.sort(%s)", query, strings.Join(escaped, ", "))
	}
	return query
}

func mustJSON(in interface{}) (out []byte) {
	out, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	return out
}

func queryLog(status *db.QueryStatus) {
	diff := status.End.Sub(status.Start)

	slowQuery := false
	if diff >= time.Millisecond*100 {
		status.Err = db.ErrWarnSlowQuery
		slowQuery = true
	}

	if status.Err != nil || slowQuery {
		db.LC().Warn(status)
		return
	}

	db.LC().Debug(status)
}

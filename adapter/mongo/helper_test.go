// Copyright (c) 2012-today The upper.io/db authors. All rights reserved.
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
	"fmt"
	"os"

	db "github.com/upper/db/v4"

	"github.com/upper/db/v4/internal/testsuite"

	"go.mongodb.org/mongo-driver/mongo"
)

var settings = ConnectionURL{
	Database: os.Getenv("DB_NAME"),
	User:     os.Getenv("DB_USERNAME"),
	Password: os.Getenv("DB_PASSWORD"),
	Host:     os.Getenv("DB_HOST") + ":" + os.Getenv("DB_PORT"),
}

type Helper struct {
	sess db.Session
}

func (h *Helper) Session() db.Session {
	return h.sess
}

func (h *Helper) Adapter() string {
	return "mongo"
}

func (h *Helper) TearDown() error {
	return h.sess.Close()
}

func (h *Helper) SetUp() error {
	ctx := context.Background()

	var err error

	h.sess, err = Open(settings)
	if err != nil {
		return err
	}

	mgdb, ok := h.sess.Driver().(*mongo.Client)
	if !ok {
		panic("expecting *mongo.Client")
	}

	var col *mongo.Collection
	col = mgdb.Database(settings.Database).Collection("birthdays")
	_ = col.Drop(ctx)

	col = mgdb.Database(settings.Database).Collection("fibonacci")
	_ = col.Drop(ctx)

	col = mgdb.Database(settings.Database).Collection("is_even")
	_ = col.Drop(ctx)

	col = mgdb.Database(settings.Database).Collection("CaSe_TesT")
	_ = col.Drop(ctx)

	// Getting a pointer to the "artist" collection.
	artist := h.sess.Collection("artist")

	_ = artist.Truncate()
	for i := 0; i < 999; i++ {
		_, err = artist.Insert(artistType{
			Name: fmt.Sprintf("artist-%d", i),
		})
		if err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}

	return nil
}

var _ testsuite.Helper = &Helper{}

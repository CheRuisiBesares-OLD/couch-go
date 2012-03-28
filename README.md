# What is couch-go?

Couch-go is a simple CouchDB (0.9+) API for the Google Go language. It supports basic operations on documents.

[![Build Status][1]][2]

[1]: https://secure.travis-ci.org/peterbourgon/couch-go.png
[2]: http://www.travis-ci.org/peterbourgon/couch-go

# Installation
Install [Go](http://www.golang.org). Then,

```
$ go get code.google.com/p/couch-go
```

As with all `go get`-ted packages, you'll need to use a specially-formatted `import` line

```
import "code.google.com/p/couch-go"
```

# Basic usage

First, create a Database object to represent the DB you'll be interacting with

```
db, err := couch.NewDatabase("127.0.0.1", "5984", "databasename")
```

A CouchDB document is represented by a Go struct

```
type Record struct {
    Type     string
    Count    uint64
    Elements []string
    MyMap    map[string]string
}
```

You can Insert these documents directly

```
r := Record{...}
id, rev, err := db.Insert(r)
```

and Retrieve them by ID.

```
r := Record{}
rev, err := db.Retrieve(id, &r)
```

If you want to specify document IDs, rather than have them auto-generated, you can use the InsertWith function

```
r := Record{...}
_, rev, err := db.InsertWith(r, "record_001")
```

or embed the `_id` in your document structure

```
type CompleteRecord struct {
    Id string `json:"_id"`
    Rev string `json:"_rev"` // useful only for Retrieve and Edit
    Foo string
    Count uint64
}
r := CompleteRecord{Id:"id001", Rev:"", Foo:"abc", Count:0}
_, rev, err := db.Insert(r)
```

You can also Edit a document, either by manually specifying the id and current revision

```
r := Record{}
currentRev, err := db.Retrieve(id, &r)
r.Count = r.Count + 1
nextRev, err := db.EditWith(r, id, currentRev)
if err != nil {
    // edit failed, out of sync?
}
```

or, as with Insert, by embedding `_id` and `_rev` fields in the document structure

```
r = CompleteRecord{} // has Id and Rev
currentRev, err := db.Retrieve(id, &r)
r.Count = r.Count + 1
nextRev, err := db.Edit(&r)
if err != nil {
    // edit failed, out of sync?
}
```

Delete works as expected

```
if err := db.Delete(id, currentRev); err != nil {
    // delete failed, out of sync?
}
```

You can also perform simple queries on views

```
func (p Database) Query(view string, options map[string]interface{}) ([]string, os.Error)
```

Check the source for more details.

# Gotchas

The behavior of the Insert and Edit classes of functions depend on the contents of the struct you pass to them.

```
   Function       Struct contains           Behavior   
   -------------  ------------------------  ------------------------------------------------
   Insert            both _id and _rev      same as Edit (attempts overwrite)   
                     only _id               same as InsertWith(id)   
                       no _id               autogenerates new id   
   InsertWith     neither _id nor _rev      uses passed id   
                   either _id  or _rev      undefined   
   Edit              both _id and _rev      will attempt to overwrite existing doc   
                  missing _id  or _rev      error   
   EditWith        either _id  or _rev      struct values overwritten by explicit values   
```



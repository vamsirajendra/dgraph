// +build !oss

/*
 * Copyright 2018 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Dgraph Community License (the "License"); you
 * may not use this file except in compliance with the License. You
 * may obtain a copy of the License at
 *
 *     https://github.com/dgraph-io/dgraph/blob/master/licenses/DCL.txt
 */

package acl

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dgraph-io/dgo"
	"github.com/dgraph-io/dgo/protos/api"
	"github.com/dgraph-io/dgraph/x"
	"github.com/golang/glog"
	"github.com/stretchr/testify/require"
)

const (
	userid         = "alice"
	userpassword   = "simplepassword"
	dgraphEndpoint = "localhost:9180"
)

func checkOutput(t *testing.T, cmd *exec.Cmd, shouldFail bool) string {
	out, err := cmd.CombinedOutput()
	if (!shouldFail && err != nil) || (shouldFail && err == nil) {
		t.Errorf("Error output from command:%v", string(out))
		t.Fatal(err)
	}

	return string(out)
}

func TestCreateAndDeleteUsers(t *testing.T) {
	// clean up the user to allow repeated running of this test
	cleanUserCmd := exec.Command("dgraph", "acl", "userdel", "-d", dgraphEndpoint,
		"-u", userid, "-x", "password")
	cleanUserCmd.Run()
	glog.Infof("cleaned up db user state")

	createUserCmd1 := exec.Command("dgraph", "acl", "useradd", "-d", dgraphEndpoint, "-u", userid,
		"-p", userpassword, "-x", "password")
	checkOutput(t, createUserCmd1, false)

	createUserCmd2 := exec.Command("dgraph", "acl", "useradd", "-d", dgraphEndpoint, "-u", userid,
		"-p", userpassword, "-x", "password")
	// create the user again should fail
	checkOutput(t, createUserCmd2, true)

	// delete the user
	deleteUserCmd := exec.Command("dgraph", "acl", "userdel", "-d", dgraphEndpoint, "-u", userid,
		"-x", "password")
	checkOutput(t, deleteUserCmd, false)

	// now we should be able to create the user again
	createUserCmd3 := exec.Command("dgraph", "acl", "useradd", "-d", dgraphEndpoint, "-u", userid,
		"-p", userpassword, "-x", "password")
	checkOutput(t, createUserCmd3, false)
}

func resetUser(t *testing.T) {
	// delete and recreate the user to ensure a clean state
	deleteUserCmd := exec.Command("dgraph", "acl", "userdel", "-d", dgraphEndpoint,
		"-u", userid, "-x", "password")
	deleteUserCmd.Run()
	glog.Infof("deleted user")

	createUserCmd := exec.Command("dgraph", "acl", "useradd", "-d", dgraphEndpoint, "-u",
		userid, "-p", userpassword, "-x", "password")
	checkOutput(t, createUserCmd, false)
	glog.Infof("created user")
}

func TestAuthorization(t *testing.T) {
	glog.Infof("testing with port 9180")
	dg1, cancel := x.GetDgraphClientOnPort(9180)
	defer cancel()
	testAuthorization(t, dg1)
	glog.Infof("done")

	glog.Infof("testing with port 9182")
	dg2, cancel := x.GetDgraphClientOnPort(9182)
	defer cancel()
	testAuthorization(t, dg2)
	glog.Infof("done")
}

func testAuthorization(t *testing.T, dg *dgo.Dgraph) {
	createAccountAndData(t, dg)
	ctx := context.Background()
	if err := dg.Login(ctx, userid, userpassword); err != nil {
		t.Fatalf("unable to login using the account %v", userid)
	}

	alterReservedPredicates(t, dg)
	queryPredicateWithUserAccount(t, dg, true)
	mutatePredicateWithUserAccount(t, dg, true)
	alterPredicateWithUserAccount(t, dg, true)

	createGroupAndAcls(t)
	// wait for 35 seconds to ensure the new acl have reached all acl caches
	log.Println("Sleeping for 35 seconds for acl to catch up")
	time.Sleep(35 * time.Second)

	alterReservedPredicates(t, dg)
	queryPredicateWithUserAccount(t, dg, false)
	// sleep long enough (10s per the docker-compose.yml in this directory)
	// for the accessJwt to expire in order to test auto login through refresh jwt
	log.Println("Sleeping for 12 seconds for accessJwt to expire")
	time.Sleep(12 * time.Second)
	mutatePredicateWithUserAccount(t, dg, false)
	log.Println("Sleeping for 12 seconds for accessJwt to expire")
	time.Sleep(12 * time.Second)
	alterPredicateWithUserAccount(t, dg, false)
}

var predicateToRead = "predicate_to_read"
var queryAttr = "name"
var predicateToWrite = "predicate_to_write"
var predicateToAlter = "predicate_to_alter"
var group = "dev"
var rootDir = filepath.Join(os.TempDir(), "acl_test")

func alterReservedPredicates(t *testing.T, dg *dgo.Dgraph) {
	ctx := context.Background()
	err := dg.Alter(ctx, &api.Operation{
		Schema: "dgraph.xid: int .",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(),
		"predicate dgraph.xid is reserved and is not allowed to be modified")

	err = dg.Alter(ctx, &api.Operation{
		DropAttr: "dgraph.xid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(),
		"predicate dgraph.xid is reserved and is not allowed to be dropped")
}

func queryPredicateWithUserAccount(t *testing.T, dg *dgo.Dgraph, shouldFail bool) {
	// login with alice's account
	ctx := context.Background()
	txn := dg.NewTxn()
	query := fmt.Sprintf(`
	{
		q(func: eq(%s, "SF")) {
			%s
		}
	}`, predicateToRead, queryAttr)
	txn = dg.NewTxn()
	_, err := txn.Query(ctx, query)

	if shouldFail {
		require.Error(t, err, "the query should have failed")
	} else {
		require.NoError(t, err, "the query should have succeeded")
	}
}

func mutatePredicateWithUserAccount(t *testing.T, dg *dgo.Dgraph, shouldFail bool) {
	ctx := context.Background()
	txn := dg.NewTxn()
	_, err := txn.Mutate(ctx, &api.Mutation{
		CommitNow: true,
		SetNquads: []byte(fmt.Sprintf(`_:a <%s>  "string" .`, predicateToWrite)),
	})

	if shouldFail {
		require.Error(t, err, "the mutation should have failed")
	} else {
		require.NoError(t, err, "the mutation should have succeeded")
	}
}

func alterPredicateWithUserAccount(t *testing.T, dg *dgo.Dgraph, shouldFail bool) {
	ctx := context.Background()
	err := dg.Alter(ctx, &api.Operation{
		Schema: fmt.Sprintf(`%s: int .`, predicateToAlter),
	})
	if shouldFail {
		require.Error(t, err, "the alter should have failed")
	} else {
		require.NoError(t, err, "the alter should have succeeded")
	}
}

func createAccountAndData(t *testing.T, dg *dgo.Dgraph) {
	// use the groot account to clean the database
	ctx := context.Background()
	if err := dg.Login(ctx, x.GrootId, "password"); err != nil {
		t.Fatalf("unable to login using the groot account:%v", err)
	}
	op := api.Operation{
		DropAll: true,
	}
	if err := dg.Alter(ctx, &op); err != nil {
		t.Fatalf("Unable to cleanup db:%v", err)
	}
	require.NoError(t, dg.Alter(ctx, &api.Operation{
		Schema: fmt.Sprintf(`%s: string @index(exact) .`, predicateToRead),
	}))

	// create some data, e.g. user with name alice
	resetUser(t)

	txn := dg.NewTxn()
	_, err := txn.Mutate(ctx, &api.Mutation{
		SetNquads: []byte(fmt.Sprintf("_:a <%s> \"SF\" .", predicateToRead)),
	})
	require.NoError(t, err)
	require.NoError(t, txn.Commit(ctx))
}

func createGroupAndAcls(t *testing.T) {
	// create a new group
	createGroupCmd := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "groupadd",
		"-d", dgraphEndpoint,
		"-g", group, "-x", "password")
	if err := createGroupCmd.Run(); err != nil {
		t.Fatalf("Unable to create group:%v", err)
	}

	// add the user to the group
	addUserToGroupCmd := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "usermod",
		"-d", dgraphEndpoint,
		"-u", userid, "-g", group, "-x", "password")
	if err := addUserToGroupCmd.Run(); err != nil {
		t.Fatalf("Unable to add user %s to group %s:%v", userid, group, err)
	}

	// add READ permission on the predicateToRead to the group
	addReadPermCmd1 := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "chmod",
		"-d", dgraphEndpoint,
		"-g", group, "-p", predicateToRead, "-P", strconv.Itoa(int(Read.Code)), "-x",
		"password")
	if err := addReadPermCmd1.Run(); err != nil {
		t.Fatalf("Unable to add READ permission on %s to group %s:%v",
			predicateToRead, group, err)
	}

	// also add read permission to the attribute queryAttr, which is used inside the query block
	addReadPermCmd2 := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "chmod",
		"-d", dgraphEndpoint,
		"-g", group, "-p", queryAttr, "-P", strconv.Itoa(int(Read.Code)), "-x",
		"password")
	if err := addReadPermCmd2.Run(); err != nil {
		t.Fatalf("Unable to add READ permission on %s to group %s:%v", queryAttr, group, err)
	}

	// add WRITE permission on the predicateToWrite
	addWritePermCmd := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "chmod",
		"-d", dgraphEndpoint,
		"-g", group, "-p", predicateToWrite, "-P", strconv.Itoa(int(Write.Code)), "-x",
		"password")
	if err := addWritePermCmd.Run(); err != nil {
		t.Fatalf("Unable to add permission on %s to group %s:%v", predicateToWrite, group, err)
	}

	// add MODIFY permission on the predicateToAlter
	addModifyPermCmd := exec.Command(os.ExpandEnv("$GOPATH/bin/dgraph"),
		"acl", "chmod",
		"-d", dgraphEndpoint,
		"-g", group, "-p", predicateToAlter, "-P", strconv.Itoa(int(Modify.Code)), "-x",
		"password")
	if err := addModifyPermCmd.Run(); err != nil {
		t.Fatalf("Unable to add permission on %s to group %s:%v", predicateToAlter, group, err)
	}
}

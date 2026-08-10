package identity_test

import (
	"context"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/customer"
	"github.com/QuantumNous/new-api/claw-control/internal/domain"
	"github.com/QuantumNous/new-api/claw-control/internal/identity"
	"github.com/QuantumNous/new-api/claw-control/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestADPAccountCannotBeBoundAcrossCustomers(t *testing.T) {
	db, err := testutil.NewDatabase()
	require.NoError(t, err)
	customers := customer.New(db, "prod", testutil.NewIdentityVerifier("v1.test"))
	identities := identity.New(db)
	firstCustomer, err := customers.Create(customer.CreateCommand{CustomerCode: "first", DisplayName: "First", Actor: "test"})
	require.NoError(t, err)
	secondCustomer, err := customers.Create(customer.CreateCommand{CustomerCode: "second", DisplayName: "Second", Actor: "test"})
	require.NoError(t, err)
	firstMember, err := customers.AddMember(context.Background(), customer.AddMemberCommand{CustomerID: firstCustomer.ID, NewAPIUserID: 101, Role: "owner", Actor: "test"})
	require.NoError(t, err)
	secondMember, err := customers.AddMember(context.Background(), customer.AddMemberCommand{CustomerID: secondCustomer.ID, NewAPIUserID: 202, Role: "owner", Actor: "test"})
	require.NoError(t, err)

	commands := []identity.ConfirmCommand{
		{BindingID: firstMember.Identity.PublicID, CanonicalSubject: firstMember.Identity.CanonicalSubject, ADPAccountID: "shared-adp-account", ADPAccountVersion: 1, Actor: "adp-backend"},
		{BindingID: secondMember.Identity.PublicID, CanonicalSubject: secondMember.Identity.CanonicalSubject, ADPAccountID: "shared-adp-account", ADPAccountVersion: 1, Actor: "adp-backend"},
	}
	errors := make([]error, len(commands))
	var wait sync.WaitGroup
	for index := range commands {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errors[index] = identities.Confirm(commands[index])
		}(index)
	}
	wait.Wait()

	successes := 0
	conflicts := 0
	for _, err := range errors {
		if err == nil {
			successes++
			continue
		}
		var domainErr *domain.Error
		require.ErrorAs(t, err, &domainErr)
		if domainErr.Kind == domain.KindConflict {
			conflicts++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

package game

import (
	"fmt"
	"io/ioutil"

	"github.com/davecgh/go-spew/spew"
	"github.com/zond/diplicity/auth"
	"github.com/zond/godip/variants"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"

	. "github.com/zond/goaeoas"
	dip "github.com/zond/godip/common"
)

var MemberResource = &Resource{
	Create:     createMember,
	Delete:     deleteMember,
	Update:     updateMember,
	CreatePath: "/Game/{game_id}/Member",
	FullPath:   "/Game/{game_id}/Member/{user_id}",
}

type Member struct {
	User             auth.User
	Nation           dip.Nation
	GameAlias        string `methods:"POST,PUT"`
	NewestPhaseState PhaseState
	UnreadMessages   int
}

func (m *Member) Item(r Request) *Item {
	return NewItem(m).SetName(m.User.Name)
}

func (m *Member) Redact(viewer *auth.User, isMember bool) {
	if !isMember {
		m.User.Email = ""
	}
	if viewer.Id != m.User.Id {
		m.GameAlias = ""
		m.NewestPhaseState = PhaseState{}
		m.UnreadMessages = 0
	}
}

func updateMember(w ResponseWriter, r Request) (*Member, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return nil, HTTPErr{"unauthorized", 401}
	}

	if user.Id != r.Vars()["user_id"] {
		return nil, HTTPErr{"can only delete yourself", 403}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return nil, err
	}

	bodyBytes, err := ioutil.ReadAll(r.Req().Body)
	if err != nil {
		return nil, err
	}
	var member *Member
	if err := datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		game := &Game{}
		if err := datastore.Get(ctx, gameID, game); err != nil {
			return HTTPErr{"non existing game", 412}
		}
		game.ID = gameID
		isMember := false
		member, isMember = game.GetMember(user.Id)
		if !isMember {
			return HTTPErr{"non existing member", 404}
		}
		if err := CopyBytes(member, r, bodyBytes, "PUT"); err != nil {
			return err
		}
		updated := false
		for i := range game.Members {
			if game.Members[i].Nation == member.Nation && game.Members[i].User.Id == member.User.Id {
				game.Members[i] = *member
				updated = true
				break
			}
		}
		if !updated {
			return fmt.Errorf("Sanity check failed, didn't succeed in finding the right member to update? game: %v, member: %v", spew.Sdump(game), spew.Sdump(member))
		}
		return game.Save(ctx)
	}, &datastore.TransactionOptions{XG: false}); err != nil {
		return nil, err
	}

	return member, nil
}

func deleteMember(w ResponseWriter, r Request) (*Member, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return nil, HTTPErr{"unauthorized", 401}
	}

	if user.Id != r.Vars()["user_id"] {
		return nil, HTTPErr{"can only delete yourself", 403}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return nil, err
	}

	var member *Member
	if err := datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		game := &Game{}
		if err := datastore.Get(ctx, gameID, game); err != nil {
			return HTTPErr{"non existing game", 412}
		}
		game.ID = gameID
		isMember := false
		member, isMember = game.GetMember(user.Id)
		if !isMember {
			return HTTPErr{"can only leave member games", 404}
		}
		if !game.Leavable() {
			return HTTPErr{"game not leavable", 412}
		}
		newMembers := []Member{}
		for _, oldMember := range game.Members {
			if oldMember.User.Id != member.User.Id {
				newMembers = append(newMembers, oldMember)
			}
		}
		if len(newMembers) == 0 && !game.Started {
			return datastore.Delete(ctx, gameID)
		}
		game.Members = newMembers
		return game.Save(ctx)
	}, &datastore.TransactionOptions{XG: false}); err != nil {
		return nil, err
	}

	return member, nil
}

func createMember(w ResponseWriter, r Request) (*Member, error) {
	ctx := appengine.NewContext(r.Req())

	user, ok := r.Values()["user"].(*auth.User)
	if !ok {
		return nil, HTTPErr{"unauthorized", 401}
	}

	gameID, err := datastore.DecodeKey(r.Vars()["game_id"])
	if err != nil {
		return nil, err
	}

	game := &Game{}
	if err := datastore.Get(ctx, gameID, game); err != nil {
		return nil, err
	}
	filterList := Games{*game}
	if _, err := filterList.RemoveBanned(ctx, user.Id); err != nil {
		return nil, err
	}
	if len(filterList) == 0 {
		return nil, HTTPErr{"banned from this game", 403}
	}

	member := &Member{}
	if err := Copy(member, r, "POST"); err != nil {
		return nil, err
	}

	if err := datastore.RunInTransaction(ctx, func(ctx context.Context) error {
		game := &Game{}
		if err := datastore.Get(ctx, gameID, game); err != nil {
			return HTTPErr{"non existing game", 412}
		}
		game.ID = gameID
		isMember := false
		_, isMember = game.GetMember(user.Id)
		if isMember {
			return HTTPErr{"user already member", 400}
		}
		if !game.Joinable() {
			return HTTPErr{"game not joinable", 412}
		}
		member.User = *user
		member.NewestPhaseState = PhaseState{
			GameID: gameID,
		}
		game.Members = append(game.Members, *member)
		if len(game.Members) == len(variants.Variants[game.Variant].Nations) {
			if err := game.Start(ctx, r); err != nil {
				return err
			}
		}
		return game.Save(ctx)
	}, &datastore.TransactionOptions{XG: true}); err != nil {
		return nil, err
	}

	return member, nil
}

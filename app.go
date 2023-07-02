package slackapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/slack-go/slack"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	COMMAND_HELP         string = "/help_goodpoint"
	COMMAND_GIVE         string = "/give_goodpoint"
	COMMAND_SHOW_RANKING string = "/show_goodpoint_ranking"
)

// point transaction
type pointTransaction struct {
	SenderId     string    `firestore:"sender_id"`
	RecieverId   string    `firestore:"reciever_id"`
	RecieverName string    `firestore:"reciever_name"`
	Reason       string    `firestore:"reason"`
	Points       int       `firestore:"points"`
	CreatedAt    time.Time `firestore:"created_at"`
	UpdatedAt    time.Time `firestore:"updated_at"`
}

// user
type user struct {
	Name   string `firestore:"name"`
	Points int    `firestore:"points"`
}

// Slack verification token.
var verificationToken string

// Define a global variable to be reused in the next function call.
// reference:https://cloud.google.com/functions/docs/bestpractices/tips#use_global_variables_to_reuse_objects_in_future_invocations
func init() {
	verificationToken = os.Getenv("VERIFICATION_TOKEN")
}

// CloudFunction entry point.
func HandleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slashCommand, err := slack.SlashCommandParse(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !slashCommand.ValidateToken(verificationToken) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	var message string
	// Create firebase client.
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "good-point-dev")

	if err != nil {
		http.Error(w, "Error creating Firestore client", http.StatusInternalServerError)
		return
	}
	defer client.Close()

	switch slashCommand.Command {
	case COMMAND_HELP:
		message = handleHelpGoodpoint(slashCommand)
	case COMMAND_GIVE:
		ms, err := handleGiveGoodPoint(ctx, client, slashCommand)
		if err != nil {
			log.Println(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		message = ms
	case COMMAND_SHOW_RANKING:
		limit := 10
		ms, err := handleShowRanking(ctx, client, limit)
		if err != nil {
			http.Error(w, "Error inquire goddpoint ranking", http.StatusInternalServerError)
			return
		}
		message = ms
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	b, err := createSlackMsg(message)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func createSlackMsg(message string) ([]byte, error) {
	// The default value for `ResponseType` is `ephemeral`, which means that the message will only be visible to the person who posted it.
	// To post a message to the entire channel, specify `in_channel`.
	params := &slack.Msg{
		ResponseType: "in_channel",
		Text:         message,
	}
	return json.Marshal(params)
}

// handle shash command `/hello_goodpoint`
func handleHelpGoodpoint(slashCommand slack.SlashCommand) string {
	var bf bytes.Buffer
	bf.WriteString(fmt.Sprintf("`%s `  コマンド一覧。\n", COMMAND_HELP))
	bf.WriteString(fmt.Sprintf("`%s @someone {give reasone}`  @someoneに対するいいねポイントの付与と理由。対象ユーザーに1ポイント付与されます。\n", COMMAND_GIVE))
	bf.WriteString(fmt.Sprintf("`%s`  今月の総獲得いいねポイントのランキング表示\n", COMMAND_SHOW_RANKING))
	return bf.String()
}

// handle shash command `/give_goodpoint`
func handleGiveGoodPoint(ctx context.Context, client *firestore.Client, slashCommand slack.SlashCommand) (string, error) {
	ptx, err := extractPointTransaction(slashCommand)
	if err != nil {
		return "", fmt.Errorf("slashCommand is invalid formats. an error occurred: %w", err)
	}
	// grant 1 pt to target user.
	if err := savePointTransaction(ctx, client, ptx); err != nil {
		return "", fmt.Errorf("failed to give point. an error occurred: %w", err)
	}
	point, err := saveUserForGrant(ctx, client, ptx)
	if err != nil {
		return "", fmt.Errorf("failed to save point. an error occurred: %w", err)
	}
	return fmt.Sprintf(
		"<@%s>さんが<@%s>さんにイイねポイントを付与しました！ \n\n【付与理由】\n %s \n【獲得ポイント数】\n %v pt",
		ptx.SenderId, ptx.RecieverId, ptx.Reason, point), nil
}

func handleShowRanking(ctx context.Context, client *firestore.Client, limit int) (string, error) {
	// get users from firestore
	query := client.Collection("users").OrderBy("points", firestore.Desc).Limit(limit)
	iter := query.Documents(ctx)

	var bf bytes.Buffer
	bf.WriteString("ランキング結果発表！\n\n")

	var i int
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return "", err
		}
		var user user
		doc.DataTo(&user)
		// add message
		bf.WriteString(fmt.Sprintf("第%v位・・・<@%s>さん【獲得ポイント】%v pt \n", i+1, doc.Ref.ID, user.Points))
		i++
	}

	bf.WriteString("\n\n")
	bf.WriteString("いつもありがとうございます！皆さん拍手をお送りください👏👏")
	return bf.String(), nil
}

func extractPointTransaction(slashCommand slack.SlashCommand) (*pointTransaction, error) {
	// example:slashCommand.Text="<@U123456789|firstname.lastname> reasons"
	targetName, err := extractTargetUserName(slashCommand.Text)
	if err != nil {
		return nil, err
	}
	targetId, err := extractTargetID(slashCommand.Text)
	if err != nil {
		return nil, err
	}
	reason, err := extractReason(slashCommand.Text)
	if err != nil {
		return nil, err
	}
	return &pointTransaction{
		SenderId:     slashCommand.UserID,
		RecieverId:   targetId,
		RecieverName: targetName,
		Reason:       reason,
		Points:       1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func extractTargetUserName(input string) (string, error) {
	re := regexp.MustCompile(`\|([^>]+)>`)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 2 || matches[1] == "" {
		return "", fmt.Errorf("failed to extract user name from input. input = %v", input)
	}
	return matches[1], nil
}

func extractTargetID(input string) (string, error) {
	re := regexp.MustCompile(`<@(U[A-Z0-9]+)\|[^>]+>`)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 2 || matches[1] == "" {
		return "", fmt.Errorf("failed to extract sender slack ID from input. input = %v", input)
	}
	return matches[1], nil
}

func extractReason(input string) (string, error) {
	startIdx := strings.Index(input, "> ")
	if startIdx == -1 || input[startIdx+2:] == "" {
		return "", fmt.Errorf("failed to extract reasons from input. input = %v", input)
	}
	return input[startIdx+2:], nil
}

func savePointTransaction(ctx context.Context, client *firestore.Client, ptx *pointTransaction) error {
	if _, _, err := client.Collection("pointTransactions").Add(ctx, *ptx); err != nil {
		return err
	}
	return nil
}

func saveUserForGrant(ctx context.Context, client *firestore.Client, ptx *pointTransaction) (points int, err error) {
	var user user
	var currentPoint int
	ref := client.Collection("users").Doc(ptx.RecieverId)
	dsnap, err := ref.Get(ctx)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return 0, fmt.Errorf("failed to get user document: %w", err)
		}
		// 初めて付与されるユーザ
		user.Name = ptx.RecieverName
		user.Points = ptx.Points
	} else {
		if err := dsnap.DataTo(&user); err != nil {
			return 0, err
		}
		user.Points += ptx.Points
	}
	if _, err = ref.Set(ctx, user); err != nil {
		return 0, err
	}
	return currentPoint, nil
}

package slackpoint

type PointTran struct {
	SenderId   string `firestore:"sender_id"`
	RecieverId string `firestore:"reciever_id"`
	Reason     string `firestore:"reason"`
	Points     int8   `firestore:"points"`
}

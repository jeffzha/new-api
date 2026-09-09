package mobilecloudseedance

const (
	ChannelName = "Mobile Cloud Seedance"
	ModelName   = "doubao-seedance-2.0"

	createTaskPath = "/contents/generations/tasks"
	fetchTaskPath  = "/contents/generations/tasks/{task_id}"

	requestContextKey = "mobile_cloud_seedance_request"
	billingProvider   = "mobile_cloud_seedance_cny"
)

var ModelList = []string{ModelName}

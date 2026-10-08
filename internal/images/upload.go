package images

func UploadURL(host string) string {
	return "https://upload." + host + "/v1beta1/upload-async"
}

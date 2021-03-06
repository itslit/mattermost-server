// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See License.txt for license information.

package utils

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	l4g "github.com/alecthomas/log4go"
	s3 "github.com/minio/minio-go"
	"github.com/minio/minio-go/pkg/credentials"

	"github.com/mattermost/mattermost-server/model"
)

const (
	TEST_FILE_PATH = "/testfile"
)

// Similar to s3.New() but allows initialization of signature v2 or signature v4 client.
// If signV2 input is false, function always returns signature v4.
//
// Additionally this function also takes a user defined region, if set
// disables automatic region lookup.
func s3New(endpoint, accessKey, secretKey string, secure bool, signV2 bool, region string) (*s3.Client, error) {
	var creds *credentials.Credentials
	if signV2 {
		creds = credentials.NewStatic(accessKey, secretKey, "", credentials.SignatureV2)
	} else {
		creds = credentials.NewStatic(accessKey, secretKey, "", credentials.SignatureV4)
	}

	s3Clnt, err := s3.NewWithCredentials(endpoint, creds, secure, region)
	if err != nil {
		return nil, err
	}

	if *Cfg.FileSettings.AmazonS3Trace {
		s3Clnt.TraceOn(os.Stdout)
	}

	return s3Clnt, nil
}

func TestFileConnection() *model.AppError {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region
		bucket := Cfg.FileSettings.AmazonS3Bucket

		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return model.NewAppError("TestFileConnection", "Bad connection to S3 or minio.", nil, err.Error(), http.StatusInternalServerError)
		}

		exists, err := s3Clnt.BucketExists(bucket)
		if err != nil {
			return model.NewAppError("TestFileConnection", "Error checking if bucket exists.", nil, err.Error(), http.StatusInternalServerError)
		}

		if !exists {
			l4g.Warn("Bucket specified does not exist. Attempting to create...")
			err := s3Clnt.MakeBucket(bucket, region)
			if err != nil {
				l4g.Error("Unable to create bucket.")
				return model.NewAppError("TestFileConnection", "Unable to create bucket", nil, err.Error(), http.StatusInternalServerError)
			}
		}
		l4g.Info("Connection to S3 or minio is good. Bucket exists.")
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		f := []byte("testingwrite")
		if err := writeFileLocally(f, Cfg.FileSettings.Directory+TEST_FILE_PATH); err != nil {
			return model.NewAppError("TestFileConnection", "Don't have permissions to write to local path specified or other error.", nil, err.Error(), http.StatusInternalServerError)
		}
		os.Remove(Cfg.FileSettings.Directory + TEST_FILE_PATH)
		l4g.Info("Able to write files to local storage.")
	} else {
		return model.NewAppError("TestFileConnection", "No file driver selected.", nil, "", http.StatusInternalServerError)
	}

	return nil
}

func ReadFile(path string) ([]byte, *model.AppError) {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region
		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return nil, model.NewAppError("ReadFile", "api.file.read_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		bucket := Cfg.FileSettings.AmazonS3Bucket
		minioObject, err := s3Clnt.GetObject(bucket, path)
		if err != nil {
			return nil, model.NewAppError("ReadFile", "api.file.read_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		defer minioObject.Close()
		if f, err := ioutil.ReadAll(minioObject); err != nil {
			return nil, model.NewAppError("ReadFile", "api.file.read_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		} else {
			return f, nil
		}
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if f, err := ioutil.ReadFile(Cfg.FileSettings.Directory + path); err != nil {
			return nil, model.NewAppError("ReadFile", "api.file.read_file.reading_local.app_error", nil, err.Error(), http.StatusInternalServerError)
		} else {
			return f, nil
		}
	} else {
		return nil, model.NewAppError("ReadFile", "api.file.read_file.configured.app_error", nil, "", http.StatusNotImplemented)
	}
}

func MoveFile(oldPath, newPath string) *model.AppError {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region
		encrypt := false
		if *Cfg.FileSettings.AmazonS3SSE && IsLicensed() && *License().Features.Compliance {
			encrypt = true
		}
		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return model.NewAppError("moveFile", "api.file.write_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		bucket := Cfg.FileSettings.AmazonS3Bucket

		source := s3.NewSourceInfo(bucket, oldPath, nil)
		destination, err := s3.NewDestinationInfo(bucket, newPath, nil, CopyMetadata(encrypt))
		if err != nil {
			return model.NewAppError("moveFile", "api.file.write_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		if err = s3Clnt.CopyObject(destination, source); err != nil {
			return model.NewAppError("moveFile", "api.file.move_file.delete_from_s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
		if err = s3Clnt.RemoveObject(bucket, oldPath); err != nil {
			return model.NewAppError("moveFile", "api.file.move_file.delete_from_s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if err := os.MkdirAll(filepath.Dir(Cfg.FileSettings.Directory+newPath), 0774); err != nil {
			return model.NewAppError("moveFile", "api.file.move_file.rename.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		if err := os.Rename(Cfg.FileSettings.Directory+oldPath, Cfg.FileSettings.Directory+newPath); err != nil {
			return model.NewAppError("moveFile", "api.file.move_file.rename.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else {
		return model.NewAppError("moveFile", "api.file.move_file.configured.app_error", nil, "", http.StatusNotImplemented)
	}

	return nil
}

func WriteFile(f []byte, path string) *model.AppError {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region
		encrypt := false
		if *Cfg.FileSettings.AmazonS3SSE && IsLicensed() && *License().Features.Compliance {
			encrypt = true
		}

		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return model.NewAppError("WriteFile", "api.file.write_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		bucket := Cfg.FileSettings.AmazonS3Bucket
		ext := filepath.Ext(path)
		metaData := S3Metadata(encrypt, "binary/octet-stream")
		if model.IsFileExtImage(ext) {
			metaData = S3Metadata(encrypt, model.GetImageMimeType(ext))
		}

		_, err = s3Clnt.PutObjectWithMetadata(bucket, path, bytes.NewReader(f), metaData, nil)
		if err != nil {
			return model.NewAppError("WriteFile", "api.file.write_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if err := writeFileLocally(f, Cfg.FileSettings.Directory+path); err != nil {
			return err
		}
	} else {
		return model.NewAppError("WriteFile", "api.file.write_file.configured.app_error", nil, "", http.StatusNotImplemented)
	}

	return nil
}

func writeFileLocally(f []byte, path string) *model.AppError {
	if err := os.MkdirAll(filepath.Dir(path), 0774); err != nil {
		directory, _ := filepath.Abs(filepath.Dir(path))
		return model.NewAppError("WriteFile", "api.file.write_file_locally.create_dir.app_error", nil, "directory="+directory+", err="+err.Error(), http.StatusInternalServerError)
	}

	if err := ioutil.WriteFile(path, f, 0644); err != nil {
		return model.NewAppError("WriteFile", "api.file.write_file_locally.writing.app_error", nil, err.Error(), http.StatusInternalServerError)
	}

	return nil
}

func RemoveFile(path string) *model.AppError {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region

		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return model.NewAppError("RemoveFile", "utils.file.remove_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		bucket := Cfg.FileSettings.AmazonS3Bucket
		if err := s3Clnt.RemoveObject(bucket, path); err != nil {
			return model.NewAppError("RemoveFile", "utils.file.remove_file.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if err := os.Remove(Cfg.FileSettings.Directory + path); err != nil {
			return model.NewAppError("RemoveFile", "utils.file.remove_file.local.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else {
		return model.NewAppError("RemoveFile", "utils.file.remove_file.configured.app_error", nil, "", http.StatusNotImplemented)
	}

	return nil
}

func getPathsFromObjectInfos(in <-chan s3.ObjectInfo) <-chan string {
	out := make(chan string, 1)

	go func() {
		defer close(out)

		for {
			info, done := <-in

			if !done {
				break
			}

			out <- info.Key
		}
	}()

	return out
}

// Returns a list of all the directories within the path directory provided.
func ListDirectory(path string) (*[]string, *model.AppError) {
	var paths []string

	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region

		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return nil, model.NewAppError("ListDirectory", "utils.file.list_directory.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		doneCh := make(chan struct{})

		defer close(doneCh)

		bucket := Cfg.FileSettings.AmazonS3Bucket
		for object := range s3Clnt.ListObjects(bucket, path, false, doneCh) {
			if object.Err != nil {
				return nil, model.NewAppError("ListDirectory", "utils.file.list_directory.s3.app_error", nil, object.Err.Error(), http.StatusInternalServerError)
			}
			paths = append(paths, strings.Trim(object.Key, "/"))
		}
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if fileInfos, err := ioutil.ReadDir(Cfg.FileSettings.Directory + path); err != nil {
			return nil, model.NewAppError("ListDirectory", "utils.file.list_directory.local.app_error", nil, err.Error(), http.StatusInternalServerError)
		} else {
			for _, fileInfo := range fileInfos {
				if fileInfo.IsDir() {
					paths = append(paths, filepath.Join(path, fileInfo.Name()))
				}
			}
		}
	} else {
		return nil, model.NewAppError("ListDirectory", "utils.file.list_directory.configured.app_error", nil, "", http.StatusInternalServerError)
	}

	return &paths, nil
}

func RemoveDirectory(path string) *model.AppError {
	if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_S3 {
		endpoint := Cfg.FileSettings.AmazonS3Endpoint
		accessKey := Cfg.FileSettings.AmazonS3AccessKeyId
		secretKey := Cfg.FileSettings.AmazonS3SecretAccessKey
		secure := *Cfg.FileSettings.AmazonS3SSL
		signV2 := *Cfg.FileSettings.AmazonS3SignV2
		region := Cfg.FileSettings.AmazonS3Region

		s3Clnt, err := s3New(endpoint, accessKey, secretKey, secure, signV2, region)
		if err != nil {
			return model.NewAppError("RemoveDirectory", "utils.file.remove_directory.s3.app_error", nil, err.Error(), http.StatusInternalServerError)
		}

		doneCh := make(chan struct{})

		bucket := Cfg.FileSettings.AmazonS3Bucket
		for err := range s3Clnt.RemoveObjects(bucket, getPathsFromObjectInfos(s3Clnt.ListObjects(bucket, path, true, doneCh))) {
			if err.Err != nil {
				doneCh <- struct{}{}
				return model.NewAppError("RemoveDirectory", "utils.file.remove_directory.s3.app_error", nil, err.Err.Error(), http.StatusInternalServerError)
			}
		}

		close(doneCh)
	} else if *Cfg.FileSettings.DriverName == model.IMAGE_DRIVER_LOCAL {
		if err := os.RemoveAll(Cfg.FileSettings.Directory + path); err != nil {
			return model.NewAppError("RemoveDirectory", "utils.file.remove_directory.local.app_error", nil, err.Error(), http.StatusInternalServerError)
		}
	} else {
		return model.NewAppError("RemoveDirectory", "utils.file.remove_directory.configured.app_error", nil, "", http.StatusNotImplemented)
	}

	return nil
}

func S3Metadata(encrypt bool, contentType string) map[string][]string {
	metaData := make(map[string][]string)
	if contentType != "" {
		metaData["Content-Type"] = []string{"contentType"}
	}
	if encrypt {
		metaData["x-amz-server-side-encryption"] = []string{"AES256"}
	}
	return metaData
}

func CopyMetadata(encrypt bool) map[string]string {
	metaData := make(map[string]string)
	metaData["x-amz-server-side-encryption"] = "AES256"
	return metaData
}

// CopyFile will copy a file from src path to dst path.
// Overwrites any existing files at dst.
// Permissions are copied from file at src to the new file at dst.
func CopyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer func() {
		if e := out.Close(); e != nil {
			err = e
		}
	}()

	_, err = io.Copy(out, in)
	if err != nil {
		return
	}

	err = out.Sync()
	if err != nil {
		return
	}

	stat, err := os.Stat(src)
	if err != nil {
		return
	}
	err = os.Chmod(dst, stat.Mode())
	if err != nil {
		return
	}

	return
}

// CopyDir will copy a directory and all contained files and directories.
// src must exist and dst must not exist.
// Permissions are preserved when possible. Symlinks are skipped.
func CopyDir(src string, dst string) (err error) {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)

	stat, err := os.Stat(src)
	if err != nil {
		return
	}
	if !stat.IsDir() {
		return fmt.Errorf("source must be a directory")
	}

	_, err = os.Stat(dst)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	if err == nil {
		return fmt.Errorf("destination already exists")
	}

	err = os.MkdirAll(dst, stat.Mode())
	if err != nil {
		return
	}

	items, err := ioutil.ReadDir(src)
	if err != nil {
		return
	}

	for _, item := range items {
		srcPath := filepath.Join(src, item.Name())
		dstPath := filepath.Join(dst, item.Name())

		if item.IsDir() {
			err = CopyDir(srcPath, dstPath)
			if err != nil {
				return
			}
		} else {
			if item.Mode()&os.ModeSymlink != 0 {
				continue
			}

			err = CopyFile(srcPath, dstPath)
			if err != nil {
				return
			}
		}
	}

	return
}

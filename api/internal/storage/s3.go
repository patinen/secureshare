package storage

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"time"
)

type Options struct {
	Endpoint, DownloadEndpoint, Region, Bucket, AccessKey, SecretKey string
	PathStyle                                                        bool
}
type S3 struct {
	client *s3.Client
	signer *s3.PresignClient
	bucket string
}

func New(ctx context.Context, o Options) (*S3, error) {
	if o.Bucket == "" || o.Region == "" {
		return nil, errors.New("S3_BUCKET and S3_REGION required")
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(o.Region)}
	if o.AccessKey != "" || o.SecretKey != "" {
		if o.AccessKey == "" || o.SecretKey == "" {
			return nil, errors.New("both S3 credentials required")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.AccessKey, o.SecretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, errors.New("storage configuration failed")
	}
	makeClient := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(cfg, func(s *s3.Options) {
			s.UsePathStyle = o.PathStyle
			if endpoint != "" {
				s.BaseEndpoint = aws.String(endpoint)
			}
			s.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			s.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		})
	}
	endpoint := o.DownloadEndpoint
	if endpoint == "" {
		endpoint = o.Endpoint
	}
	return &S3{client: makeClient(o.Endpoint), signer: s3.NewPresignClient(makeClient(endpoint)), bucket: o.Bucket}, nil
}
func (s *S3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	// Always store as attachment/octet-stream as well as overriding signed GET.
	// The untrusted normalized type is metadata in PostgreSQL, never inline content.
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String("application/octet-stream"), ContentDisposition: aws.String("attachment"), CacheControl: aws.String("no-store")})
	return err
}
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
func (s *S3) PresignGet(ctx context.Context, key, filename string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > DownloadTTL {
		return "", errors.New("invalid download lifetime")
	}
	signed, err := s.signer.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ResponseContentType: aws.String("application/octet-stream"), ResponseContentDisposition: aws.String(Disposition(filename)), ResponseCacheControl: aws.String("no-store")}, func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return "", err
	}
	return signed.URL, nil
}

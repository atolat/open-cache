variable "region" {
  default = "us-east-1"
}

variable "bucket_name" {
  default = "open-cache-bazel"
}

variable "vpc_id" {
  default = "vpc-02861cdb0ed9aabef"
}

variable "route_table_ids" {
  default = [
    "rtb-0328b016260502958",
    "rtb-09fc6ef258d102ab8",
    "rtb-042c9329a2e162196",
  ]
}

variable "domain" {
  default = "open-cache.io"
}
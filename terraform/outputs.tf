output "bucket_name" {
  value = aws_s3_bucket.cache.id
}

output "bucket_regional_domain" {
  value = aws_s3_bucket.cache.bucket_regional_domain_name
}

output "vpc_endpoint_id" {
  value = aws_vpc_endpoint.s3.id
}

output "route53_nameservers" {
  value       = aws_route53_zone.main.name_servers
  description = "Set these as nameservers in Namecheap for open-cache.io"
}

output "certificate_arn" {
  value = aws_acm_certificate.cache.arn
}

output "route53_zone_id" {
  value = aws_route53_zone.main.zone_id
}

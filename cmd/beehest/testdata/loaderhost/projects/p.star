project(repo = "o/n", forge = "github", build_host = "lane", base_branch = "main", gate = "make check", test_report = "r.xml", host = host(provider = "ssh", disk_floor_gb = 50))

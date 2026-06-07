test:
	go run . | tee oracle.txt
	git status

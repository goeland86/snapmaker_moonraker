pipeline {
    agent { label 'snapmaker-builder' }
    environment {
        GOFLAGS = '-trimpath'
    }
    stages {
        stage('Build Go Binaries') {
            steps {
                sh '''
                    echo "==> Building ARM (RPi) binary..."
                    GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
                        go build -ldflags="-s -w" -o snapmaker_moonraker-armv7 .

                    echo "==> Building Linux x86_64 binary..."
                    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
                        go build -ldflags="-s -w" -o snapmaker_moonraker_Linux.bin .

                    echo "==> Building Windows x86_64 binary..."
                    GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
                        go build -ldflags="-s -w" -o snapmaker_moonraker_Win.exe .

                    echo "==> Building macOS ARM64 binary..."
                    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
                        go build -ldflags="-s -w" -o snapmaker_moonraker_Mac.bin .

                    file snapmaker_moonraker-armv7
                    file snapmaker_moonraker_Linux.bin
                    file snapmaker_moonraker_Win.exe
                    file snapmaker_moonraker_Mac.bin
                '''
            }
        }
        stage('Build RPi Image') {
            steps {
                sh 'sudo image/build-image.sh snapmaker_moonraker-armv7'
            }
        }
        stage('Publish to GitHub Release') {
            when {
                expression {
                    return sh(script: 'git describe --exact-match --tags HEAD 2>/dev/null', returnStatus: true) == 0
                }
            }
            environment {
                GITHUB_TOKEN = credentials('github-token')
            }
            steps {
                sh '''
                    # Get the tag name for this commit
                    TAG_NAME=$(git describe --exact-match --tags HEAD)
                    echo "Detected tag: ${TAG_NAME}"

                    # Extract release notes for this tag from Release_Notes.md.
                    # Grabs everything between "## <tag>" (or "## <tag> —") and the next "---" separator.
                    NOTES=""
                    if [ -f Release_Notes.md ]; then
                        NOTES=$(awk -v tag="${TAG_NAME}" '
                            BEGIN { found=0 }
                            /^## / {
                                if (found) exit
                                if (index($0, tag) > 0) found=1
                                next
                            }
                            /^---$/ { if (found) exit }
                            found { print }
                        ' Release_Notes.md)
                    fi

                    # Create release — use extracted notes or fall back to auto-generated notes.
                    if [ -n "$NOTES" ]; then
                        echo "$NOTES" > /tmp/release-notes.md
                        gh release create "${TAG_NAME}" \
                            --repo goeland86/snapmaker_moonraker \
                            --title "Snapmaker Moonraker ${TAG_NAME}" \
                            --notes-file /tmp/release-notes.md \
                        || echo "Release ${TAG_NAME} already exists, uploading artifacts..."
                    else
                        echo "No release notes found for ${TAG_NAME}, using auto-generated notes"
                        gh release create "${TAG_NAME}" \
                            --repo goeland86/snapmaker_moonraker \
                            --title "Snapmaker Moonraker ${TAG_NAME}" \
                            --generate-notes \
                        || echo "Release ${TAG_NAME} already exists, uploading artifacts..."
                    fi

                    # Upload artifacts (--clobber overwrites if they exist)
                    gh release upload "${TAG_NAME}" \
                        --repo goeland86/snapmaker_moonraker \
                        --clobber \
                        snapmaker-moonraker-rpi3-*.img.xz \
                        snapmaker_moonraker_Linux.bin \
                        snapmaker_moonraker_Win.exe \
                        snapmaker_moonraker_Mac.bin
                '''
            }
        }
    }
    post {
        always {
            archiveArtifacts artifacts: 'snapmaker-moonraker-rpi3-*.img.xz', allowEmptyArchive: true
            cleanWs()
        }
    }
}

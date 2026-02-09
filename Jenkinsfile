pipeline {
    agent { label 'snapmaker-builder' }
    environment {
        GOFLAGS = '-trimpath'
    }
    stages {
        stage('Build Go Binary') {
            steps {
                sh '''
                    GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
                        go build -ldflags="-s -w" -o snapmaker_moonraker-armv7 .
                    file snapmaker_moonraker-armv7
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

                    # Create release if it doesn't exist
                    gh release create "${TAG_NAME}" \
                        --repo goeland86/snapmaker_moonraker \
                        --title "Snapmaker Moonraker ${TAG_NAME}" \
                        --generate-notes \
                    || echo "Release ${TAG_NAME} already exists, uploading artifacts..."

                    # Upload artifacts (--clobber overwrites if they exist)
                    gh release upload "${TAG_NAME}" \
                        --repo goeland86/snapmaker_moonraker \
                        --clobber \
                        snapmaker-moonraker-rpi3-*.img.xz
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

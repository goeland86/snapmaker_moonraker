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
            when { buildingTag() }
            environment {
                GITHUB_TOKEN = credentials('github-token')
            }
            steps {
                sh '''
                    gh release create "${TAG_NAME}" \
                        --repo goeland86/snapmaker_moonraker \
                        --title "Snapmaker Moonraker ${TAG_NAME}" \
                        --generate-notes \
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

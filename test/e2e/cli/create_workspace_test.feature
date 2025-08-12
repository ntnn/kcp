Feature: I create a workspace

    Scenario: I create a workspace
        I have a shared server
        I run the "create-workspace" plugin with args "test-workspace"
        Then the workspace "test-workspace" should exist

    Scenario: I create a workspace with an existing name
        I have a shared server
        I run the "create-workspace" plugin with args "test-workspace-existing"
        Then the workspace "test-workspace" should exist
        When I run the "create-workspace" plugin with args "test-workspace-existing"
        Then I should see an error message "Workspace already exists"

    Scenario: I create a workspace and enter it
        I have a shared server
        I run the "create-workspace" plugin with args "test-workspace-enter"
        Then the workspace "test-workspace" should exist
        When I run the "ws" plugin with args ". --short"
        Then I should see the output "test-workspace-enter"
